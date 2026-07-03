package researcher

import (
	"context"
	"fmt"
	"strings"

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
// 失败时返回错误，由调用方决定是否回退到默认角色。
func ChooseRole(ctx context.Context, l *llm.LLM, query string) (string, error) {
	role, err := l.Chat(ctx, prompts.ChooseAgentSystemPrompt(), prompts.ChooseAgentUserPrompt(query))
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
type Engine struct {
	cfg        *config.Config
	llm        *llm.LLM
	researcher *Researcher
}

// NewEngine 创建顶层研究引擎。
func NewEngine(cfg *config.Config, l *llm.LLM, r *Researcher) *Engine {
	return &Engine{cfg: cfg, llm: l, researcher: r}
}

// FinalReport 是 Run 的最终产出（含报告正文与来源列表）。
type FinalReport struct {
	Markdown string   `json:"markdown"` // 完整 Markdown 报告
	Sources  []Source `json:"sources"`  // 来源列表
}

// Run 执行完整研究流程：选角色 → 收集资料 → 撰写报告。
//
// onProgress 可为 nil；非 nil 时用于上报各阶段进度。
// 查询参数 opts 可覆盖默认配置（如 MaxIterations），为零值时沿用 cfg。
func (e *Engine) Run(ctx context.Context, query string, opts *Options, onProgress EventFn, onReportChunk ReportChunkFn) (*FinalReport, error) {
	emit := func(p Progress) {
		if onProgress != nil {
			onProgress(p)
		}
	}

	// ---- 阶段 1：选角色 ----
	role, err := ChooseRole(ctx, e.llm, query)
	if err != nil {
		// 角色生成失败不致命，退化为通用研究员。
		role = "你是一名严谨的研究助理，擅长基于资料客观地撰写研究报告。"
		emit(Progress{Stage: StageRole, Message: fmt.Sprintf("角色生成失败，使用默认角色: %v", err)})
	} else {
		emit(Progress{Stage: StageRole, Message: role})
	}

	// ---- 阶段 2：收集资料 ----
	res, err := e.researcher.Conduct(ctx, query, onProgress)
	if err != nil {
		return nil, err
	}

	// ---- 阶段 3：流式撰写报告 ----
	emit(Progress{Stage: StageWriting, Message: "正在撰写报告……"})

	// 用流式生成：LLM 边生成边吐块，连接持续有数据流动，避免等完整大响应被判 idle 超时。
	// 每个块通过 onReportChunk 实时推给前端（对齐 gpt-researcher 的 stream=True）。
	ch, err := e.llm.ChatStream(ctx,
		prompts.ReportSystemPrompt(role, e.cfg.Language),
		prompts.ReportUserPrompt(query, res.Context, e.cfg.TotalWords),
	)
	if err != nil {
		return nil, fmt.Errorf("撰写报告失败（流式启动）: %w", err)
	}

	var reportBuilder strings.Builder
	for chunk := range ch {
		reportBuilder.WriteString(chunk)
		if onReportChunk != nil {
			onReportChunk(chunk, reportBuilder.String())
		}
	}
	report := reportBuilder.String()
	if strings.TrimSpace(report) == "" {
		return nil, fmt.Errorf("撰写报告失败: 模型返回空内容")
	}

	// 若报告中缺少"参考资料"段，补一份来源清单（保证可溯源）。
	if !strings.Contains(report, "参考资料") && len(res.Sources) > 0 {
		report = strings.TrimRight(report, "\n") + "\n\n## 参考资料\n"
		for _, s := range res.Sources {
			report += fmt.Sprintf("%d. %s — %s\n", s.N, s.Title, s.URL)
		}
	}

	return &FinalReport{Markdown: report, Sources: res.Sources}, nil
}

// Options 允许在单次 Run 中覆盖默认配置（未来扩展用，当前预留）。
type Options struct {
	MaxIterations *int
	TotalWords    *int
}
