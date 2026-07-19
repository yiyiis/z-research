// Package deep — engine.go 是深度递归引擎，实现 researcher.EngineIface。
//
// 与单 Agent / 多 Agent / ReAct 并列，是第 4 种研究模式（mode="deep"）。
// 在服务启动时构造一次（编译 graph 开销大），Run 时反复 Invoke。
package deep

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/compose"

	"z-research/backend/internal/collection"
	"z-research/backend/internal/config"
	"z-research/backend/internal/llm"
	"z-research/backend/internal/researcher"
)

// Engine 是深度递归研究引擎。
type Engine struct {
	cfg   *config.Config
	llm   *llm.LLM
	inner *researcher.Researcher

	// runnable 是 5 节点 deep graph 的编译产物。
	runnable compose.Runnable[string, string]
}

// 编译期断言：*Engine 实现 researcher.EngineIface。
var _ researcher.EngineIface = (*Engine)(nil)

// NewEngine 创建深度递归引擎，编译 5 节点 graph。
// 编译失败会 panic（启动期错误）。
func NewEngine(cfg *config.Config, l *llm.LLM, r *researcher.Researcher) *Engine {
	g := BuildDeepGraph(context.Background(), cfg, l, r)
	runnable, err := g.Compile(context.Background(), compose.WithMaxRunSteps(cfg.MaxRunSteps))
	if err != nil {
		panic(fmt.Errorf("compile deep graph: %w", err))
	}
	return &Engine{cfg: cfg, llm: l, inner: r, runnable: runnable}
}

// Run 执行深度递归研究流程。
//
// 通过 5 节点 Graph 编排：
//
//	START → choose_role → plan_search → deep_recurse(Lambda,递归) → compress → writer → END
//
// per-run 的 query、breadth、depth、回调通过 WithInitialState 注入 context。
func (e *Engine) Run(
	ctx context.Context,
	query string,
	opts *researcher.Options,
	onProgress researcher.EventFn,
	onReportChunk researcher.ReportChunkFn,
) (*researcher.FinalReport, error) {
	breadth := e.cfg.DeepBreadth
	depth := e.cfg.DeepDepth
	totalWords := e.cfg.TotalWords
	if opts != nil {
		// 允许 per-run 覆盖 breadth/depth（Phase 6 前端会传入）。
		if opts.Breadth != nil && *opts.Breadth > 0 {
			breadth = *opts.Breadth
		}
		if opts.Depth != nil && *opts.Depth >= 0 {
			depth = *opts.Depth
		}
	}

	initial := &DeepState{
		Query:          query,
		Breadth:        breadth,
		Depth:          depth,
		TotalWords:     totalWords,
		OnProgress:     onProgress,
		OnReportChunk:  onReportChunk,
		Visited:        collection.NewVisitedSet(),
	}
	ctx = WithInitialState(ctx, initial)

	out, err := e.runnable.Invoke(ctx, query)
	if err != nil {
		return nil, err
	}
	sources := []collection.Source(nil)
	if initial.Visited != nil {
		sources = initial.Visited.All()
	}
	return &researcher.FinalReport{Markdown: out, Sources: sources}, nil
}
