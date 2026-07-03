package researcher

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	"z-research/backend/internal/compress"
	"z-research/backend/internal/config"
	"z-research/backend/internal/llm"
	"z-research/backend/internal/prompts"
	"z-research/backend/internal/scraper"
	"z-research/backend/internal/search"
)

// Researcher 是研究编排器，实现 gpt-researcher 默认的固定工作流。
type Researcher struct {
	cfg    *config.Config
	llm    *llm.LLM
	search *search.Searcher
}

// NewResearcher 创建研究编排器。
func NewResearcher(cfg *config.Config, llm *llm.LLM, search *search.Searcher) *Researcher {
	return &Researcher{cfg: cfg, llm: llm, search: search}
}

// collector 封装 Conduct 过程中跨子查询共享的可变状态（替代裸指针传递）。
// 所有字段都由 mu 保护。
type collector struct {
	mu            sync.Mutex
	srcCounter    int            // 全局来源编号计数器
	sources       []Source       // 去重后的来源列表
	urlToSourceID map[string]int // url → 引用编号（去重）
}

// registerSource 登记一个 URL，返回它的引用编号（已存在则返回旧编号）。
func (c *collector) registerSource(url, title string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if id, ok := c.urlToSourceID[url]; ok {
		return id
	}
	c.srcCounter++
	id := c.srcCounter
	c.urlToSourceID[url] = id
	c.sources = append(c.sources, Source{N: id, URL: url, Title: title})
	return id
}

// Conduct 完成资料收集阶段，返回带来源编号的参考资料文本与来源列表。
//
// onProgress 可为 nil；非 nil 时用于上报各阶段进度（供 CLI/HTTP 复用）。
// 它不调用报告生成 LLM；报告生成由 Engine.Run 完成（职责分离）。
func (r *Researcher) Conduct(ctx context.Context, query string, onProgress EventFn) (*Result, error) {
	emit := func(p Progress) {
		if onProgress != nil {
			onProgress(p)
		}
	}

	// ---- 阶段 1：规划子查询 ----
	subQueries, err := r.planSubQueries(ctx, query)
	if err != nil {
		return nil, err
	}
	// 始终把原始查询也作为一条子查询（对齐 gpt-researcher）。
	subQueries = appendUnique(subQueries, query)
	emit(Progress{Stage: StagePlanning, Message: fmt.Sprintf("规划完成，共 %d 个子查询", len(subQueries))})

	// ---- 阶段 2：并发执行每个子查询的 {search → fetch → compress} ----
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(r.cfg.Concurrency)

	col := &collector{urlToSourceID: make(map[string]int)}
	var (
		bMu           sync.Mutex
		contextBlocks []string
	)

	for _, sq := range subQueries {
		sq := sq
		g.Go(func() error {
			block, n := r.processSubQuery(gctx, sq, col, emit)
			if block != "" {
				bMu.Lock()
				contextBlocks = append(contextBlocks, block)
				bMu.Unlock()
			}
			emit(Progress{Stage: StageSearching, SubQuery: sq, Found: n, Message: fmt.Sprintf("子查询 %q 完成，抓取 %d 个网页", sq, n)})
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, fmt.Errorf("并发研究阶段出错: %w", err)
	}

	// ---- 阶段 3：合并上下文 ----
	if len(contextBlocks) == 0 {
		return nil, fmt.Errorf("未能收集到任何有效资料，请检查网络或更换查询后重试")
	}
	contextStr := strings.Join(contextBlocks, "\n\n")
	return &Result{Context: contextStr, Sources: col.sources}, nil
}

// planSubQueries 用 LLM 把查询拆成 N 个子查询。
func (r *Researcher) planSubQueries(ctx context.Context, query string) ([]string, error) {
	var subQueries []string
	if err := r.llm.ChatJSON(ctx,
		prompts.SubQuerySystemPrompt(),
		prompts.SubQueryUserPrompt(query, r.cfg.MaxIterations),
		&subQueries,
	); err != nil {
		return nil, fmt.Errorf("生成子查询失败: %w", err)
	}

	// 兜底：若 LLM 没给出有效子查询，退化为只用原查询。
	cleaned := make([]string, 0, len(subQueries))
	for _, sq := range subQueries {
		if s := strings.TrimSpace(sq); s != "" {
			cleaned = append(cleaned, s)
		}
	}
	if len(cleaned) == 0 {
		cleaned = []string{query}
	}
	return cleaned, nil
}

// processSubQuery 处理单个子查询：搜索 → 抓取 → 压缩 → 拼成带来源编号的文本块。
//
// 它通过 col 安全地登记共享来源，返回最终的文本块与抓取网页数。
// 注意：任何单个网页的失败都不应中断整体研究，故内部错误被记录后吞掉。
func (r *Researcher) processSubQuery(
	ctx context.Context,
	subQuery string,
	col *collector,
	emit EventFn,
) (string, int) {
	// 1. 搜索
	results, err := r.search.Search(ctx, subQuery, r.cfg.MaxResultsPerQuery)
	if err != nil {
		emit(Progress{Stage: StageSearching, SubQuery: subQuery, Message: fmt.Sprintf("搜索失败: %v", err)})
		return "", 0
	}
	if len(results) == 0 {
		return "", 0
	}

	// 2. 截取前 MaxScrapePerQuery 个结果并发抓取。
	max := r.cfg.MaxScrapePerQuery
	if max > len(results) {
		max = len(results)
	}

	type fetched struct {
		page *scraper.ScrapedPage
		id   int
	}
	var (
		fmu      sync.Mutex
		fetcheds []fetched
		wg       sync.WaitGroup
		sem      = make(chan struct{}, max)
	)
	for i := 0; i < max; i++ {
		res := results[i]
		url := strings.TrimSpace(res.URL)
		if url == "" {
			continue
		}
		id := col.registerSource(url, res.Title)

		wg.Add(1)
		sem <- struct{}{}
		go func(res search.SearchResult, id int) {
			defer wg.Done()
			defer func() { <-sem }()
			emit(Progress{Stage: StageFetching, SubQuery: subQuery, URL: res.URL})
			page, err := scraper.FetchURL(ctx, res.URL)
			if err != nil {
				emit(Progress{Stage: StageFetching, URL: res.URL, Message: fmt.Sprintf("抓取失败: %v", err)})
				return
			}
			fmu.Lock()
			fetcheds = append(fetcheds, fetched{page: page, id: id})
			fmu.Unlock()
		}(res, id)
	}
	wg.Wait()

	if len(fetcheds) == 0 {
		return "", 0
	}

	// 3. 把每个网页单独压缩，并拼成 "Source / Title / Content" 块。
	var b strings.Builder
	for _, f := range fetcheds {
		emit(Progress{Stage: StageCompressing, SubQuery: subQuery, URL: f.page.URL})
		compressed, err := compress.Compress(ctx, r.llm.Embedder(),
			subQuery, f.page.Content,
			r.cfg.SimilarityThreshold, 5, r.cfg.CompressionThreshold)
		if err != nil {
			emit(Progress{Stage: StageCompressing, URL: f.page.URL, Message: fmt.Sprintf("压缩失败: %v", err)})
			continue
		}
		if strings.TrimSpace(compressed) == "" {
			continue
		}
		fmt.Fprintf(&b, "Source: [%d] %s\n", f.id, f.page.URL)
		if f.page.Title != "" {
			fmt.Fprintf(&b, "Title: %s\n", f.page.Title)
		}
		fmt.Fprintf(&b, "Content: %s\n\n", strings.TrimSpace(compressed))
	}
	return b.String(), len(fetcheds)
}

// appendUnique 把 s 追加到 list（若非空且不存在），返回新切片。
func appendUnique(list []string, s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return list
	}
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return list
		}
	}
	return append(list, s)
}
