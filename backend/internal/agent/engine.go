package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"

	"z-research/backend/internal/config"
	"z-research/backend/internal/llm"
	"z-research/backend/internal/prompts"
	"z-research/backend/internal/researcher"
	"z-research/backend/internal/search"
)

// maxAgentSteps 是 ReAct 循环的最大迭代次数（LLM 出 tool_call → 执行 → 回灌 算一步）。
// 太小可能没研究完就强制停止；太大则成本高。研究任务通常需要 10-20 步。
const maxAgentSteps = 25

// AgentEngine 是 ReAct Agent 引擎，实现 researcher.EngineIface。
//
// 与 single（确定性工作流）的本质区别：
//   - single：代码写死 plan→search→fetch→compress→write，LLM 只做内容生成。
//   - Agent：LLM 自主控制循环（ReAct），自主决定搜什么、抓哪个、何时停止。
//
// 底层用 Eino 的 flow/agent/react.NewAgent——它内部构建 chat↔tools 的图，
// LLM 输出 tool_call 时自动执行工具并回灌，直到 LLM 不再调工具（产出最终答案）。
type AgentEngine struct {
	cfg   *config.Config
	agent *react.Agent
}

// 编译期断言：*AgentEngine 实现 researcher.EngineIface。
var _ researcher.EngineIface = (*AgentEngine)(nil)

// NewAgentEngine 构造一个 ReAct Agent 引擎。
//
// 它把 DuckDuckGo 搜索 + 网页抓取封装成两个工具（web_search / fetch_url），
// 绑给 smart 档 LLM，让 LLM 自主研究。
func NewAgentEngine(ctx context.Context, cfg *config.Config, l *llm.LLM, searcher *search.Searcher) (*AgentEngine, error) {
	// 1. 构造两个工具。
	searchTool, err := NewSearchTool(searcher)
	if err != nil {
		return nil, fmt.Errorf("创建搜索工具失败: %w", err)
	}
	fetchTool, err := NewFetchTool()
	if err != nil {
		return nil, fmt.Errorf("创建抓取工具失败: %w", err)
	}

	// 2. 构造 ReAct Agent。
	//    ToolCallingModel 用 smart 档（写报告需要质量）。
	//    MessageModifier 注入研究指令的 system prompt。
	//    MaxStep 限制最大迭代次数。
	a, err := react.NewAgent(ctx, &react.AgentConfig{
		ToolCallingModel: l.SmartModel(),
		ToolsConfig: compose.ToolsNodeConfig{
			Tools: []tool.BaseTool{searchTool, fetchTool},
		},
		MaxStep: maxAgentSteps,
		MessageModifier: func(_ context.Context, msgs []*schema.Message) []*schema.Message {
			// 在消息前插入 system prompt，指导 LLM 如何做研究。
			sys := schema.SystemMessage(agentSystemPrompt(cfg.Language))
			return append([]*schema.Message{sys}, msgs...)
		},
	})
	if err != nil {
		return nil, fmt.Errorf("创建 ReAct Agent 失败: %w", err)
	}

	return &AgentEngine{cfg: cfg, agent: a}, nil
}

// Run 执行 ReAct Agent 研究流程。
//
// 它把 query 作为 user 消息发给 agent，agent 内部自主跑完 ReAct 循环
// （LLM 决定调搜索/抓取工具、何时停止），最终产出报告。
//
// onProgress 上报阶段（MVP：整体上报"Agent 自主研究中"）。
// onReportChunk 把最终报告推给前端（一次性，因为 Generate 是阻塞的）。
func (e *AgentEngine) Run(ctx context.Context, query string, opts *researcher.Options, onProgress researcher.EventFn, onReportChunk researcher.ReportChunkFn) (*researcher.FinalReport, error) {
	emit := func(p researcher.Progress) {
		if onProgress != nil {
			onProgress(p)
		}
	}

	emit(researcher.Progress{Stage: researcher.StageRole, Message: "ReAct Agent 启动，LLM 将自主决定搜索与抓取策略"})
	emit(researcher.Progress{Stage: researcher.StageSearching, Message: "Agent 正在自主研究中（LLM 决定调用工具）…"})

	// 构造输入消息：user 携带研究查询 + 写报告要求。
	userMsg := fmt.Sprintf("%s\n\n%s",
		query,
		prompts.ReportUserPrompt(query, "（由 Agent 自主收集的资料，无需在此重复）", e.cfg.TotalWords),
	)

	// 调 agent.Generate——内部跑完 ReAct 循环，返回最终消息。
	finalMsg, err := e.agent.Generate(ctx, []*schema.Message{
		schema.UserMessage(userMsg),
	})
	if err != nil {
		return nil, fmt.Errorf("Agent 研究失败: %w", err)
	}

	report := finalMsg.Content
	if strings.TrimSpace(report) == "" {
		return nil, fmt.Errorf("Agent 返回空报告")
	}

	emit(researcher.Progress{Stage: researcher.StageWriting, Message: "Agent 研究完成，报告已生成"})

	// 推送最终报告给前端（一次性，Agent 的 Generate 是阻塞的）。
	if onReportChunk != nil {
		onReportChunk(report, report)
	}

	// Agent 模式下来源列表由 LLM 在报告里自行标注（它知道抓取过哪些 URL）。
	// 若报告缺"参考资料"段，补一个提示。
	if !strings.Contains(report, "参考资料") && !strings.Contains(report, "References") {
		report = strings.TrimRight(report, "\n") + "\n\n> 注：本报告由 ReAct Agent 自主研究生成，来源由 Agent 在研究过程中引用。"
	}

	return &researcher.FinalReport{Markdown: report, Sources: nil}, nil
}

// agentSystemPrompt 是 ReAct Agent 的系统指令。
// 它告诉 LLM：你是一个自主研究 Agent，有 web_search 和 fetch_url 两个工具，
// 要自主决定如何研究，最后输出结构化报告。
func agentSystemPrompt(language string) string {
	lang := "中文"
	if strings.EqualFold(language, "english") || language == "en" {
		lang = "English"
	}
	return fmt.Sprintf(`你是一个自主研究 Agent。你有两个工具可用：

1. web_search：在互联网上搜索信息，返回相关网页的标题、URL 和摘要。
2. fetch_url：抓取指定 URL 的网页正文内容。

研究流程（由你自主决策，不是固定步骤）：
- 根据用户的研究问题，决定搜索什么关键词；
- 搜索后，判断哪些网页值得深入阅读，调用 fetch_url 抓取正文；
- 根据抓取到的内容，决定是否需要继续搜索其他角度、或换关键词补充；
- 当你认为收集的资料足够回答问题时，停止调用工具，直接撰写最终报告。

报告要求：
- 用 %s 撰写，Markdown 格式；
- 结构清晰，包含标题、正文章节、结论；
- 内容基于你通过工具收集的真实资料，不要编造；
- 引用具体信息时标注来源 URL；
- 客观、严谨。

重要：你要自主判断"资料是否足够"，不要无限制地搜索。通常搜索 2-4 次、抓取 3-6 个网页就足够了。资料够就立即写报告。`, lang)
}
