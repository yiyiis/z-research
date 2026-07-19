package researcher

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/compose"

	"z-research/backend/internal/collection"
	"z-research/backend/internal/config"
	"z-research/backend/internal/llm"
	"z-research/backend/internal/prompts"
)

// ReportChunkFn 在流式写报告时，每个生成块被调用。
// chunk 是本次新生成的一段文本（token），accu 是到目前为止累积的完整报告。
// 用于 WebSocket handler 把报告逐块实时推给前端。
type ReportChunkFn func(chunk string, accu string)

// EngineIface 抽象 Engine.Run，供 api 包依赖注入与测试替身使用
// （api 包不直接依赖具体 *Engine，便于用假引擎做 HTTP 测试）。
type EngineIface interface {
	Run(ctx context.Context, query string, opts *Options, onProgress EventFn, onReportChunk ReportChunkFn) (*FinalReport, error)
}

// 编译期断言：*Engine 实现 EngineIface。
var _ EngineIface = (*Engine)(nil)

// ChooseRole 让 LLM 根据查询给出领域专家角色设定（纯文本，非 JSON）。
// 用 fast 档位（小任务，对应 gpt-researcher 的 choose_agent 用 fast_llm）。
func ChooseRole(ctx context.Context, l *llm.LLM, query string) (string, error) {
	role, err := l.FastChat(ctx, prompts.ChooseAgentSystemPrompt(), prompts.ChooseAgentUserPrompt(query))
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(role) == "" {
		return "", fmt.Errorf("空角色")
	}
	return role, nil
}

// Engine 把"选角色 → 收集资料 → 撰写报告"三步封装成可复用的顶层流程，
// 供 CLI 与 HTTP handler 共用。它通过 Progress 回调上报各阶段进度。
//
// 单 Agent 简报流程（RunBrief）通过一个 5 节点 Eino compose.Graph 编排：
//
//	START → choose_role → plan_search → parallel_research → compression → writer → END
//
// 该 graph 在 NewEngine 时 Compile 一次（开销大），Run 时反复 Invoke。
// 详细报告流程（RunDetailed）仍是串行多轮拆分，未走 graph（保留旧行为）。
type Engine struct {
	cfg        *config.Config
	llm        *llm.LLM
	researcher *Researcher
	// runnable 是单 Agent 简报流程的 5 节点 graph 编译产物。
	runnable compose.Runnable[string, string]
}

// NewEngine 创建顶层研究引擎，并编译单 Agent 5 节点 graph。
// 编译失败会 panic（启动期错误，应尽早暴露）。
func NewEngine(cfg *config.Config, l *llm.LLM, r *Researcher) *Engine {
	g := BuildSingleGraph(context.Background(), cfg, l, r)
	runnable, err := g.Compile(context.Background(), compose.WithMaxRunSteps(cfg.MaxRunSteps))
	if err != nil {
		panic(fmt.Errorf("compile single-agent graph: %w", err))
	}
	return &Engine{cfg: cfg, llm: l, researcher: r, runnable: runnable}
}

// FinalReport 是 Run 的最终产出（含报告正文与来源列表）。
type FinalReport struct {
	Markdown string   `json:"markdown"` // 完整 Markdown 报告
	Sources  []Source `json:"sources"`  // 来源列表
}

// Run 执行完整研究流程，按 opts.ReportType 分派：
//   - ReportBrief（默认）：选角色 → 收集资料 → 单次流式撰写简报。
//   - ReportDetailed：选角色 → 收集初步资料 → 生成大纲 → 逐章独立检索+流式撰写 → 引言+结论 → 拼接。
//
// onProgress 可为 nil；非 nil 时用于上报各阶段进度。
// 查询参数 opts 可覆盖默认配置（如 MaxIterations），为零值时沿用 cfg。
func (e *Engine) Run(ctx context.Context, query string, opts *Options, onProgress EventFn, onReportChunk ReportChunkFn) (*FinalReport, error) {
	if opts != nil && opts.ReportType == ReportDetailed {
		return e.RunDetailed(ctx, query, opts, onProgress, onReportChunk)
	}
	return e.RunBrief(ctx, query, onProgress, onReportChunk)
}

// RunBrief 执行简报流程：通过 5 节点 Eino compose.Graph 编排
//
//	START → choose_role → plan_search → parallel_research → compression → writer → END
//
// Graph 在 NewEngine 时编译一次，这里反复 Invoke。per-run 的 query、
// 回调、来源列表通过 WithInitialState 注入 context，由 choose_role
// 节点的 StatePreHandler 拷进 graph 的 per-run *ResearchState。
//
// 由于 graph 的输出只有 string（最终报告正文），Sources 无法直接从
// Invoke 返回值取出。这里通过把 sources 闭包捕获到 initial state 里，
// parallel_research 节点写入 state.Visited 后我们再读 Visited.All() 拿回。
func (e *Engine) RunBrief(ctx context.Context, query string, onProgress EventFn, onReportChunk ReportChunkFn) (*FinalReport, error) {
	// initial state 通过 context 注入 graph。
	initial := &ResearchState{
		Query:          query,
		TotalWords:     e.cfg.TotalWords,
		OnProgress:     onProgress,
		OnReportChunk:  onReportChunk,
		Visited:        nil, // pre-handler 会创建
	}
	ctx = WithInitialState(ctx, initial)

	report, err := e.runnable.Invoke(ctx, query)
	if err != nil {
		return nil, err
	}
	// 从共享 VisitedSet 拿回最终来源列表。
	sources := []collection.Source(nil)
	if initial.Visited != nil {
		sources = initial.Visited.All()
	}
	return &FinalReport{Markdown: report, Sources: sources}, nil
}

// Options 允许在单次 Run 中覆盖默认配置（未来扩展用，当前预留）。
type Options struct {
	MaxIterations *int
	TotalWords    *int

	// ReportType 选择报告类型：ReportBrief（简报，默认）/ ReportDetailed（详细，多轮拆分）。
	// 留空走 ReportBrief。单 Agent Engine 据此分派；multiagent 引擎忽略此字段。
	ReportType ReportType

	// Mode selects the engine implementation. nil or
	// ModeSingle = the original single-agent Engine.
	// ModeMulti = the multi-agent Engine (see
	// internal/multiagent package). Single-agent Engine
	// ignores this field.
	Mode *string

	// HumanFeedbackFn is invoked by the multi-agent
	// engine's human_review node when EnableHITL is true
	// in config. The callback receives the current plan
	// (title + sections) and returns the user's free-form
	// feedback (empty string = accept, non-empty = revise).
	// Single-agent Engine ignores this field.
	//
	// The callback runs synchronously and may block for
	// the duration of a human's review (e.g. waiting on
	// a websocket message). The caller is responsible
	// for honoring ctx cancellation.
	HumanFeedbackFn HumanFeedbackFn

	// TaskID is a stable identifier for this run, used by
	// the multi-agent engine to persist + restore
	// checkpoint state (via Eino's WithCheckPointStore).
	// If empty, the multi-agent engine generates a
	// random one. Single-agent Engine ignores this field.
	TaskID string

	// EnableHITL overrides cfg.EnableHITL for this run.
	// When true (and the engine is multi-agent), the
	// human_review node blocks until the user accepts
	// or revises the plan. nil = use cfg.EnableHITL.
	// The api layer reads this from ResearchRequest.HitL
	// (set by the frontend HITL checkbox).
	EnableHITL *bool
}

// HumanFeedbackFn is the callback signature the multi-agent
// engine uses to ask the user for a plan review. The
// implementation is provided by the API layer (typically by
// reading a websocket message after writing the
// "human_feedback" frame).
//
// The callback returns:
//   - feedback: free-form text. Empty means "accept the plan
//     as-is". Non-empty means "revise" with these instructions.
//   - err: non-nil aborts the run.
type HumanFeedbackFn func(ctx context.Context, plan HumanReviewPlan) (feedback string, err error)

// HumanReviewPlan is the snapshot the multi-agent engine
// hands to the HumanFeedbackFn callback.
type HumanReviewPlan struct {
	Title    string
	Sections []string
	Revision int // 0 on first review, 1 on first revise, etc.
}
