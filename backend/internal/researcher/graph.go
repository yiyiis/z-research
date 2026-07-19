// Package researcher — graph.go 把单 Agent 研究流程装配为 Eino compose.Graph。
//
// 拓扑（对齐面试话术里的"单 Agent 5 节点线性 Graph"）：
//
//	START → choose_role → plan_search → parallel_research → compression → writer → END
//
// 每个节点都是一个 compose.InvokableLambda，通过 compose.ProcessState 读写
// per-run 的 *ResearchState。choose_role 节点挂载 StatePreHandler 把
// context 里 stash 的 per-run 初始状态（query、回调）注入 graph state。
//
// Graph 的 I/O 类型是 string（最终报告 Markdown）。报告生成仍走流式：
// writer 节点内部调用 llm.ChatStream，每个 chunk 通过 OnReportChunk 回调
// 实时推给前端（对齐 gpt-researcher 的 stream=True）。
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

// 单 Agent Graph 的节点 key 常量。
const (
	NodeChooseRole      = "choose_role"
	NodePlanSearch      = "plan_search"
	NodeParallelResearch = "parallel_research"
	NodeCompression     = "compression"
	NodeWriter          = "writer"
)

// injectInitialStatePreHandler 把 context 里的 per-run 初始状态
// 注入 graph 的 *ResearchState。只挂在 choose_role（图的首节点）上即可，
// 因为后续节点看到的 state 已经填好了。
var injectInitialStatePreHandler = compose.StatePreHandler[string, *ResearchState](
	func(ctx context.Context, in string, state *ResearchState) (string, error) {
		initial := InitialStateFromContext(ctx)
		if initial == nil || state == nil {
			return in, nil
		}
		if state.Query == "" {
			state.Query = initial.Query
		}
		if state.TotalWords == 0 {
			state.TotalWords = initial.TotalWords
		}
		// 回调每次都覆盖（per-run 注入）。
		state.OnProgress = initial.OnProgress
		state.OnReportChunk = initial.OnReportChunk
		// 为 parallel_research 节点预分配共享 VisitedSet。
		if state.Visited == nil {
			state.Visited = collection.NewVisitedSet()
		}
		return in, nil
	},
)

// BuildSingleGraph 装配（但不编译）单 Agent 5 节点 Graph。
// 编译开销大，应在服务启动时 Compile 一次复用。
func BuildSingleGraph(
	ctx context.Context,
	cfg *config.Config,
	llmClient *llm.LLM,
	r *Researcher,
) *compose.Graph[string, string] {
	g := compose.NewGraph[string, string](
		compose.WithGenLocalState(func(ctx context.Context) *ResearchState {
			return &ResearchState{}
		}),
	)

	// --- 节点 1：choose_role ---
	// 用 fast 档位让 LLM 根据查询给出领域专家角色设定（对齐 gpt-researcher 的 choose_agent）。
	chooseRoleNode := compose.InvokableLambda(
		func(ctx context.Context, _ string) (string, error) {
			var query string
			_ = compose.ProcessState[*ResearchState](ctx, func(_ context.Context, s *ResearchState) error {
				query = s.Query
				return nil
			})
			role, err := ChooseRole(ctx, llmClient, query)
			if err != nil {
				// 角色生成失败不致命，退化为通用研究员。
				role = "你是一名严谨的研究助理，擅长基于资料客观地撰写研究报告。"
				if s := initialProgress(ctx); s != nil {
					s(Progress{Stage: StageRole, Message: fmt.Sprintf("角色生成失败，使用默认角色: %v", err)})
				}
			} else if s := initialProgress(ctx); s != nil {
				s(Progress{Stage: StageRole, Message: role})
			}
			_ = compose.ProcessState[*ResearchState](ctx, func(_ context.Context, st *ResearchState) error {
				st.Role = role
				return nil
			})
			return role, nil
		},
	)
	if err := g.AddLambdaNode(NodeChooseRole, chooseRoleNode,
		compose.WithNodeName(NodeChooseRole),
		compose.WithStatePreHandler(injectInitialStatePreHandler),
	); err != nil {
		panic(fmt.Errorf("add choose_role: %w", err))
	}

	// --- 节点 2：plan_search ---
	// 用 strategic 档位把查询拆成 N 个子查询（决定研究方向的杠杆点）。
	planSearchNode := compose.InvokableLambda(
		func(ctx context.Context, _ string) (string, error) {
			var query string
			_ = compose.ProcessState[*ResearchState](ctx, func(_ context.Context, s *ResearchState) error {
				query = s.Query
				return nil
			})
			// breadth=0 → 走 cfg.MaxIterations。
			subQueries, err := r.PlanSubQueries(ctx, query, 0)
			if err != nil {
				return "", err
			}
			_ = compose.ProcessState[*ResearchState](ctx, func(_ context.Context, st *ResearchState) error {
				st.SubQueries = subQueries
				return nil
			})
			if s := initialProgress(ctx); s != nil {
				s(Progress{Stage: StagePlanning, Message: fmt.Sprintf("规划完成，共 %d 个子查询", len(subQueries))})
			}
			// 把子查询数作为传递给下游的 string 数据。
			return fmt.Sprintf("%d subqueries", len(subQueries)), nil
		},
	)
	if err := g.AddLambdaNode(NodePlanSearch, planSearchNode, compose.WithNodeName(NodePlanSearch)); err != nil {
		panic(fmt.Errorf("add plan_search: %w", err))
	}

	// --- 节点 3：parallel_research ---
	// 并发执行每个子查询的 {search → fetch → compress}。
	// 抓取并发由全局 workerpool(cfg.MaxScraperWorkers) 控制。
	parallelResearchNode := compose.InvokableLambda(
		func(ctx context.Context, _ string) (string, error) {
			var (
				subQueries []string
				visited    *collection.VisitedSet
			)
			_ = compose.ProcessState[*ResearchState](ctx, func(_ context.Context, s *ResearchState) error {
				subQueries = s.SubQueries
				visited = s.Visited
				return nil
			})
			if visited == nil {
				visited = collection.NewVisitedSet()
			}
			// 复用 Researcher.RunSubQueries（errgroup + workerpool + VisitedSet）。
			res, err := r.RunSubQueries(ctx, subQueries, visited, progressFromState(ctx))
			if err != nil {
				return "", err
			}
			_ = compose.ProcessState[*ResearchState](ctx, func(_ context.Context, st *ResearchState) error {
				st.Context = res.Context
				st.Sources = res.Sources
				return nil
			})
			return res.Context, nil
		},
	)
	if err := g.AddLambdaNode(NodeParallelResearch, parallelResearchNode, compose.WithNodeName(NodeParallelResearch)); err != nil {
		panic(fmt.Errorf("add parallel_research: %w", err))
	}

	// --- 节点 4：compression ---
	// 单 Agent 简报模式：parallel_research 已经在 processSubQuery 内部对每个网页
	// 做了 embedding 压缩，这里只是"合并/规范化"步骤——目前是直通的占位节点，
	// 保留这个节点是为了与面试话术里的"compression 节点"对齐，未来可在此做
	// 跨子查询的二次压缩或上下文裁剪。
	compressionNode := compose.InvokableLambda(
		func(ctx context.Context, in string) (string, error) {
			return in, nil
		},
	)
	if err := g.AddLambdaNode(NodeCompression, compressionNode, compose.WithNodeName(NodeCompression)); err != nil {
		panic(fmt.Errorf("add compression: %w", err))
	}

	// --- 节点 5：writer ---
	// 用 smart 档位流式撰写报告，每个 chunk 通过 OnReportChunk 实时推给前端。
	writerNode := compose.InvokableLambda(
		func(ctx context.Context, _ string) (string, error) {
			var (
				query       string
				role        string
				contextStr  string
				sources     []collection.Source
				totalWords  int
				onChunk     ReportChunkFn
			)
			_ = compose.ProcessState[*ResearchState](ctx, func(_ context.Context, s *ResearchState) error {
				query = s.Query
				role = s.Role
				contextStr = s.Context
				sources = s.Sources
				totalWords = s.TotalWords
				onChunk = s.OnReportChunk
				return nil
			})
			if totalWords <= 0 {
				totalWords = cfg.TotalWords
			}
			if s := initialProgress(ctx); s != nil {
				s(Progress{Stage: StageWriting, Message: "正在撰写报告……"})
			}
			report, err := WriteReport(ctx, llmClient, role, query, contextStr, totalWords, sources, onChunk, cfg.Language)
			if err != nil {
				return "", err
			}
			_ = compose.ProcessState[*ResearchState](ctx, func(_ context.Context, st *ResearchState) error {
				st.Report = report
				return nil
			})
			return report, nil
		},
	)
	if err := g.AddLambdaNode(NodeWriter, writerNode, compose.WithNodeName(NodeWriter)); err != nil {
		panic(fmt.Errorf("add writer: %w", err))
	}

	// --- 边 ---
	edges := [][2]string{
		{compose.START, NodeChooseRole},
		{NodeChooseRole, NodePlanSearch},
		{NodePlanSearch, NodeParallelResearch},
		{NodeParallelResearch, NodeCompression},
		{NodeCompression, NodeWriter},
		{NodeWriter, compose.END},
	}
	for _, e := range edges {
		if err := g.AddEdge(e[0], e[1]); err != nil {
			panic(fmt.Errorf("edge %s->%s: %w", e[0], e[1], err))
		}
	}

	return g
}

// WriteReport 流式撰写最终报告，每个 chunk 通过 onChunk 回调实时推送。
// 这是 writer 节点（以及旧 RunBrief）共享的核心实现。
//
// 若报告中缺少"参考资料"段，会补一份来源清单（保证可溯源）。
func WriteReport(
	ctx context.Context,
	l *llm.LLM,
	role, query, contextStr string,
	totalWords int,
	sources []collection.Source,
	onChunk ReportChunkFn,
	language string,
) (string, error) {
	ch, err := l.ChatStream(ctx,
		prompts.ReportSystemPrompt(role, language),
		prompts.ReportUserPrompt(query, contextStr, totalWords),
	)
	if err != nil {
		return "", fmt.Errorf("撰写报告失败（流式启动）: %w", err)
	}
	var b strings.Builder
	for chunk := range ch {
		b.WriteString(chunk)
		if onChunk != nil {
			onChunk(chunk, b.String())
		}
	}
	report := b.String()
	if strings.TrimSpace(report) == "" {
		return "", fmt.Errorf("撰写报告失败: 模型返回空内容")
	}
	// 若报告中缺少"参考资料"段，补一份来源清单（保证可溯源）。
	if !strings.Contains(report, "参考资料") && len(sources) > 0 {
		report = strings.TrimRight(report, "\n") + "\n\n## 参考资料\n"
		for _, s := range sources {
			report += fmt.Sprintf("%d. %s — %s\n", s.N, s.Title, s.URL)
		}
	}
	return report, nil
}

// initialProgress 从 context 取出 OnProgress 回调并包装为 EventFn。
// 节点内部用它上报进度。
func initialProgress(ctx context.Context) EventFn {
	st := InitialStateFromContext(ctx)
	if st == nil || st.OnProgress == nil {
		return nil
	}
	return st.OnProgress
}

// progressFromState 与 initialProgress 等价，命名保留以匹配 graph 节点风格。
func progressFromState(ctx context.Context) EventFn {
	return initialProgress(ctx)
}
