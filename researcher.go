package main

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"
)

// Source 记录一条被引用的来源（带全局编号，用于报告中的 [n] 引用）。
type Source struct {
	N     int // 引用编号，从 1 开始
	URL   string
	Title string
}

// Researcher 是研究编排器，实现 gpt-researcher 默认的固定工作流：
//
//	plan(子查询) → 并发{search → fetch → compress} → 合并上下文
//
// 它本身不做 ReAct 循环；控制流是确定性的，深度/数量由 Config 严格限定
// （.md 文档 Turn 25 推荐的"工具层硬截断 + 规划执行分离"做法）。
type Researcher struct {
	cfg    *Config
	llm    *LLM
	search *Searcher
}

// NewResearcher 创建研究编排器。
func NewResearcher(cfg *Config, llm *LLM, search *Searcher) *Researcher {
	return &Researcher{cfg: cfg, llm: llm, search: search}
}

// Conduct 完成资料收集阶段，返回：
//   - contextStr: 供报告撰写使用的、带来源编号的参考资料文本；
//   - sources:    按发现顺序去重的来源列表（含编号）。
//
// 它不调用报告生成 LLM；报告生成由 main 调用 LLM.Chat 完成（职责分离）。
func (r *Researcher) Conduct(ctx context.Context, query string) (string, []Source, error) {
	// ---- 阶段 1：规划子查询 ----
	subQueries, err := r.planSubQueries(ctx, query)
	if err != nil {
		return "", nil, err
	}
	// 始终把原始查询也作为一条子查询（对齐 gpt-researcher：会 append 原 query）。
	subQueries = appendUnique(subQueries, query)
	logf("📋 规划完成，共 %d 个子查询: %v", len(subQueries), subQueries)

	// ---- 阶段 2：并发执行每个子查询的 {search → fetch → compress} ----
	// 用一个带并发上限的 errgroup 控制子查询并发。
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(r.cfg.Concurrency)

	// sources 在所有子查询间共享（带来源编号）；contextBlocks 收集每个子查询压缩后的文本块。
	var (
		mu            sync.Mutex
		srcCounter    int
		sources       []Source
		urlToSourceID = map[string]int{} // url -> 引用编号，去重
		contextBlocks []string           // 每个子查询压缩后的 "Source/Title/Content" 块
	)

	for _, sq := range subQueries {
		sq := sq // 捕获循环变量
		g.Go(func() error {
			block, n := r.processSubQuery(gctx, sq, &mu, &srcCounter, &sources, &urlToSourceID)
			if block != "" {
				mu.Lock()
				contextBlocks = append(contextBlocks, block)
				mu.Unlock()
			}
			logf("   ↳ 子查询 %q 完成，抓取 %d 个网页", sq, n)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return "", nil, fmt.Errorf("并发研究阶段出错: %w", err)
	}

	// ---- 阶段 3：合并上下文 ----
	if len(contextBlocks) == 0 {
		return "", nil, fmt.Errorf("未能收集到任何有效资料，请检查网络或更换查询后重试")
	}
	contextStr := strings.Join(contextBlocks, "\n\n")
	logf("✅ 资料收集完成，共 %d 个来源，参考资料约 %d 字符", len(sources), len([]rune(contextStr)))
	return contextStr, sources, nil
}

// planSubQueries 用 LLM 把查询拆成 N 个子查询。
func (r *Researcher) planSubQueries(ctx context.Context, query string) ([]string, error) {
	var subQueries []string
	if err := r.llm.ChatJSON(ctx,
		subQuerySystemPrompt(),
		subQueryUserPrompt(query, r.cfg.MaxIterations),
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
// 它通过锁安全地更新共享的 sources 列表与编号映射，返回最终的文本块与抓取网页数。
// 注意：任何单个网页的失败都不应中断整体研究，故内部错误被记录后吞掉。
func (r *Researcher) processSubQuery(
	ctx context.Context,
	subQuery string,
	mu *sync.Mutex,
	srcCounter *int,
	sources *[]Source,
	urlToSourceID *map[string]int,
) (string, int) {
	// 1. 搜索
	results, err := r.search.Search(ctx, subQuery, r.cfg.MaxResultsPerQuery)
	if err != nil {
		logf("   ⚠ 搜索 %q 失败: %v", subQuery, err)
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
		page *ScrapedPage
		id   int // 该页对应的来源编号（0 表示未登记）
	}
	var (
		fmu      sync.Mutex
		fetcheds []fetched
		wg       sync.WaitGroup
		sem      = make(chan struct{}, max) // 抓取并发上限 = 本子查询的抓取数
	)
	for i := 0; i < max; i++ {
		res := results[i]
		// 登记来源（去重），拿到编号。空 URL 跳过。
		url := strings.TrimSpace(res.URL)
		if url == "" {
			continue
		}
		var id int
		mu.Lock()
		if existing, ok := (*urlToSourceID)[url]; ok {
			id = existing
		} else {
			*srcCounter++
			id = *srcCounter
			(*urlToSourceID)[url] = id
			*sources = append(*sources, Source{N: id, URL: url, Title: res.Title})
		}
		mu.Unlock()

		wg.Add(1)
		sem <- struct{}{}
		go func(res SearchResult, id int) {
			defer wg.Done()
			defer func() { <-sem }()
			page, err := FetchURL(ctx, res.URL)
			if err != nil {
				logf("      ⚠ 抓取 %q 失败: %v", res.URL, err)
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
		compressed, err := Compress(ctx, r.llm.Embedder(),
			subQuery, f.page.Content,
			r.cfg.SimilarityThreshold, 5, r.cfg.CompressionThreshold)
		if err != nil {
			logf("      ⚠ 压缩 %q 失败: %v", f.page.URL, err)
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
