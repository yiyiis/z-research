package researcher

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	"z-research/backend/internal/collection"
	"z-research/backend/internal/compress"
	"z-research/backend/internal/config"
	"z-research/backend/internal/llm"
	"z-research/backend/internal/prompts"
	"z-research/backend/internal/scraper"
	"z-research/backend/internal/search"
	"z-research/backend/internal/workerpool"
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

// Conduct 完成资料收集阶段，返回带来源编号的参考资料文本与来源列表。
//
// onProgress 可为 nil；非 nil 时用于上报各阶段进度（供 CLI/HTTP 复用）。
// 它不调用报告生成 LLM；报告生成由 Engine.Run 完成（职责分离）。
//
// 注意：每次调用都会创建一个独立的 collection.VisitedSet，因此跨多次
// Conduct 调用不会去重。需要跨调用共享去重的场景（如详细报告跨章、
// 深度递归跨层）应使用 ConductWithVisited。
func (r *Researcher) Conduct(ctx context.Context, query string, onProgress EventFn) (*Result, error) {
	return r.ConductWithVisited(ctx, query, 0, collection.NewVisitedSet(), onProgress)
}

// ConductWithVisited 与 Conduct 行为一致，但接收一个共享的 VisitedSet，
// 允许多次调用累积去重同一批 URL。这是面试话术里 "visited_urls Set" 的
// 跨层共享入口。
//
// breadth 控制本轮子查询的数量上限（≤0 时回退到 cfg.MaxIterations）。
// 这一层是深度递归引擎逐层衰减 breadth 的接入点：max(2, breadth//2)。
func (r *Researcher) ConductWithVisited(ctx context.Context, query string, breadth int, visited *collection.VisitedSet, onProgress EventFn) (*Result, error) {
	emit := func(p Progress) {
		if onProgress != nil {
			onProgress(p)
		}
	}

	// ---- 阶段 1：规划子查询 ----
	subQueries, err := r.PlanSubQueries(ctx, query, breadth)
	if err != nil {
		return nil, err
	}
	emit(Progress{Stage: StagePlanning, Message: fmt.Sprintf("规划完成，共 %d 个子查询", len(subQueries))})

	// ---- 阶段 2/3：并发执行 + 合并上下文 ----
	return r.RunSubQueries(ctx, subQueries, visited, onProgress)
}

// PlanSubQueries 用 LLM 把查询拆成 N 个子查询（导出版本，供 graph 节点复用）。
//
// breadth ≤0 时使用 cfg.MaxIterations（默认 3）。
// 这一层支持深度递归的逐层衰减：调用方传入 max(2, prevBreadth//2)。
// 始终会把原始 query 也作为一条子查询追加（对齐 gpt-researcher）。
func (r *Researcher) PlanSubQueries(ctx context.Context, query string, breadth int) ([]string, error) {
	subQueries, err := r.planSubQueries(ctx, query, breadth)
	if err != nil {
		return nil, err
	}
	return appendUnique(subQueries, query), nil
}

// RunSubQueries 并发执行一批子查询的 {search → fetch → compress}，
// 把各自的文本块合并成最终 Context。它不调用报告生成 LLM。
//
// 这是 5 节点 Graph 的 parallel_research 节点的核心实现，
// 也供深度递归引擎的叶子研究复用。
func (r *Researcher) RunSubQueries(ctx context.Context, subQueries []string, visited *collection.VisitedSet, onProgress EventFn) (*Result, error) {
	emit := func(p Progress) {
		if onProgress != nil {
			onProgress(p)
		}
	}

	// 子查询之间的并发由 errgroup.SetLimit(cfg.Concurrency) 控制。
	// 单子查询内部的网页抓取并发由全局 workerpool(cfg.MaxScraperWorkers) 控制。
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(r.cfg.Concurrency)

	var (
		bMu           sync.Mutex
		contextBlocks []string
	)

	for _, sq := range subQueries {
		sq := sq
		g.Go(func() error {
			block, n := r.processSubQuery(gctx, sq, visited, emit)
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

	if len(contextBlocks) == 0 {
		return nil, fmt.Errorf("未能收集到任何有效资料，请检查网络或更换查询后重试")
	}
	contextStr := strings.Join(contextBlocks, "\n\n")
	return &Result{Context: contextStr, Sources: visited.All()}, nil
}

// planSubQueries 用 LLM 把查询拆成 N 个子查询（内部实现，不含 appendUnique 原始查询）。
//
// breadth ≤0 时使用 cfg.MaxIterations（默认 3）。
// 这一层支持深度递归的逐层衰减：调用方传入 max(2, prevBreadth//2)。
func (r *Researcher) planSubQueries(ctx context.Context, query string, breadth int) ([]string, error) {
	if breadth <= 0 {
		breadth = r.cfg.MaxIterations
	}
	var subQueries []string
	// 用 strategic 档位（拆子查询属于规划/拆子主题，决定研究方向）。
	if err := r.llm.StrategicChatJSON(ctx,
		prompts.SubQuerySystemPrompt(),
		prompts.SubQueryUserPrompt(query, breadth),
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
// 它通过 visited 安全地登记共享来源，返回最终的文本块与抓取网页数。
// 网页抓取的并发上限由全局 workerpool(cfg.MaxScraperWorkers=15) 控制——
// 对应面试话术 MAX_SCRAPER_WORKERS。
// 注意：任何单个网页的失败都不应中断整体研究，故内部错误被记录后吞掉。
func (r *Researcher) processSubQuery(
	ctx context.Context,
	subQuery string,
	visited *collection.VisitedSet,
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
	)
	// 网页抓取的并发由全局 workerpool 控制（cfg.MaxScraperWorkers，默认 15），
	// 这是面试话术里的 MAX_SCRAPER_WORKERS。原来用 `make(chan struct{}, max)`
	// 容量等于抓取数，等于不限流——是一个潜在并发爆炸点，现已修复。
	pool := workerpool.New(r.cfg.MaxScraperWorkers)
	for i := 0; i < max; i++ {
		res := results[i]
		url := strings.TrimSpace(res.URL)
		if url == "" {
			continue
		}
		// 跨子查询/章节/递归层共享的 VisitedSet 负责去重与编号。
		// 已抓取过的 URL 会复用旧编号，不会重复登记。
		id := visited.Register(url, res.Title)
		pool.Go(ctx, func() error {
			emit(Progress{Stage: StageFetching, SubQuery: subQuery, URL: res.URL})
			page, err := scraper.FetchURL(ctx, res.URL)
			if err != nil {
				emit(Progress{Stage: StageFetching, URL: res.URL, Message: fmt.Sprintf("抓取失败: %v", err)})
				return nil // 单个网页失败不中断整体研究
			}
			fmu.Lock()
			fetcheds = append(fetcheds, fetched{page: page, id: id})
			fmu.Unlock()
			return nil
		})
	}
	_ = pool.Wait()

	if len(fetcheds) == 0 {
		return "", 0
	}

	// 3. 把每个网页单独压缩，并拼成 "Source / Title / Content" 块。
	// 压缩并发由 cfg.MaxEmbedWorkers 控制（通过 compress.Compress 内部 workerpool）。
	var b strings.Builder
	for _, f := range fetcheds {
		emit(Progress{Stage: StageCompressing, SubQuery: subQuery, URL: f.page.URL})
		compressed, err := compress.Compress(ctx, r.llm.Embedder(),
			subQuery, f.page.Content,
			r.cfg.SimilarityThreshold, 5, r.cfg.CompressionThreshold, r.cfg.MaxEmbedWorkers)
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
