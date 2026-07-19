// Package deep — graph.go 装配深度递归的 5 节点 Eino compose.Graph。
//
// 拓扑（对齐 session 设计）：
//
//	START → choose_role → plan_search → deep_recurse(Lambda,递归) → compress → writer → END
//
// 与单 Agent graph 的关键区别在 deep_recurse 节点：它内部调用 deepRecurse()
// 递归函数（普通 Go 递归，不开新 Graph），breadth 逐层衰减 max(2, b//2)。
package deep

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/compose"

	"z-research/backend/internal/collection"
	"z-research/backend/internal/config"
	"z-research/backend/internal/llm"
	"z-research/backend/internal/researcher"
)

// 深度递归 Graph 的节点 key 常量。
const (
	NodeChooseRole  = "choose_role"
	NodePlanSearch  = "plan_search"
	NodeDeepRecurse = "deep_recurse"
	NodeCompress    = "compress"
	NodeWriter      = "writer"
)

// injectInitialStatePreHandler 把 context 里的 per-run 初始状态注入 graph。
var injectInitialStatePreHandler = compose.StatePreHandler[string, *DeepState](
	func(ctx context.Context, in string, state *DeepState) (string, error) {
		initial := InitialStateFromContext(ctx)
		if initial == nil || state == nil {
			return in, nil
		}
		if state.Query == "" {
			state.Query = initial.Query
		}
		if state.Breadth == 0 {
			state.Breadth = initial.Breadth
		}
		if state.Depth == 0 {
			state.Depth = initial.Depth
		}
		if state.TotalWords == 0 {
			state.TotalWords = initial.TotalWords
		}
		state.OnProgress = initial.OnProgress
		state.OnReportChunk = initial.OnReportChunk
		if state.Visited == nil {
			state.Visited = collection.NewVisitedSet()
		}
		return in, nil
	},
)

// BuildDeepGraph 装配（但不编译）深度递归 5 节点 Graph。
func BuildDeepGraph(
	ctx context.Context,
	cfg *config.Config,
	llmClient *llm.LLM,
	r *researcher.Researcher,
) *compose.Graph[string, string] {
	g := compose.NewGraph[string, string](
		compose.WithGenLocalState(func(ctx context.Context) *DeepState {
			return &DeepState{}
		}),
	)

	// --- 节点 1：choose_role ---
	chooseRoleNode := compose.InvokableLambda(
		func(ctx context.Context, _ string) (string, error) {
			var query string
			_ = compose.ProcessState[*DeepState](ctx, func(_ context.Context, s *DeepState) error {
				query = s.Query
				return nil
			})
			role, err := researcher.ChooseRole(ctx, llmClient, query)
			if err != nil {
				role = "你是一名严谨的研究助理，擅长基于资料客观地撰写研究报告。"
				if s := initialProgress(ctx); s != nil {
					s(researcher.Progress{Stage: researcher.StageRole, Message: fmt.Sprintf("角色生成失败，使用默认角色: %v", err)})
				}
			} else if s := initialProgress(ctx); s != nil {
				s(researcher.Progress{Stage: researcher.StageRole, Message: role})
			}
			_ = compose.ProcessState[*DeepState](ctx, func(_ context.Context, st *DeepState) error {
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
	// 用原始 query 生成第一层追问查询（基于"无 learnings"的规划）。
	planSearchNode := compose.InvokableLambda(
		func(ctx context.Context, _ string) (string, error) {
			var (
				query   string
				breadth int
			)
			_ = compose.ProcessState[*DeepState](ctx, func(_ context.Context, s *DeepState) error {
				query = s.Query
				breadth = s.Breadth
				return nil
			})
			if breadth <= 0 {
				breadth = cfg.DeepBreadth
			}
			// 第一层：没有 learnings，让 LLM 基于原始 query 直接规划。
			followups, err := generateFollowups(ctx, llmClient, query, nil, breadth)
			if err != nil {
				return "", err
			}
			// 兜底：规划失败则用原 query。
			if len(followups) == 0 {
				followups = []string{query}
			}
			_ = compose.ProcessState[*DeepState](ctx, func(_ context.Context, st *DeepState) error {
				st.InitialFollowups = followups
				return nil
			})
			if s := initialProgress(ctx); s != nil {
				s(researcher.Progress{Stage: researcher.StagePlanning, Message: fmt.Sprintf("深度递归规划完成，第一层 %d 个追问", len(followups))})
			}
			return fmt.Sprintf("%d followups", len(followups)), nil
		},
	)
	if err := g.AddLambdaNode(NodePlanSearch, planSearchNode, compose.WithNodeName(NodePlanSearch)); err != nil {
		panic(fmt.Errorf("add plan_search: %w", err))
	}

	// --- 节点 3：deep_recurse（核心：Lambda 节点内递归） ---
	// 对每个 first-layer followup 启动一棵递归子树，累积所有叶子资料。
	deepRecurseNode := compose.InvokableLambda(
		func(ctx context.Context, _ string) (string, error) {
			var (
				followups []string
				depth     int
				breadth   int
				query     string
				visited   *collection.VisitedSet
			)
			_ = compose.ProcessState[*DeepState](ctx, func(_ context.Context, s *DeepState) error {
				followups = s.InitialFollowups
				depth = s.Depth
				breadth = s.Breadth
				query = s.Query
				visited = s.Visited
				return nil
			})
			if depth <= 0 {
				depth = cfg.DeepDepth
			}
			if breadth <= 0 {
				breadth = cfg.DeepBreadth
			}
			if visited == nil {
				visited = collection.NewVisitedSet()
			}
			if s := initialProgress(ctx); s != nil {
				s(researcher.Progress{Stage: researcher.StageSearching, Message: fmt.Sprintf("开始深度递归: depth=%d, breadth=%d", depth, breadth)})
			}
			// 对每个第一层追问并发启动递归子树。
			// 注意：这里用同步串行 + 内部 workerpool，避免顶层并发与递归层并发叠加。
			// 递归内部已有 workerpool 控制扇出。
			var allBlocks []string
			for _, fq := range followups {
				sub, err := deepRecurse(ctx, r, llmClient, fq, depth, breadth, query, visited, progressFromState(ctx))
				if err != nil {
					return "", err
				}
				allBlocks = append(allBlocks, sub.contextBlocks...)
			}
			contextStr := strings.Join(allBlocks, "\n\n")
			_ = compose.ProcessState[*DeepState](ctx, func(_ context.Context, st *DeepState) error {
				st.Context = contextStr
				st.Sources = visited.All()
				return nil
			})
			return contextStr, nil
		},
	)
	if err := g.AddLambdaNode(NodeDeepRecurse, deepRecurseNode, compose.WithNodeName(NodeDeepRecurse)); err != nil {
		panic(fmt.Errorf("add deep_recurse: %w", err))
	}

	// --- 节点 4：compress ---
	// deep_recurse 已经在每个叶子做了 embedding 压缩，这里是跨子树的二次规范化。
	// 目前是直通占位（保留节点以对齐拓扑话术）。
	compressNode := compose.InvokableLambda(
		func(ctx context.Context, in string) (string, error) {
			return in, nil
		},
	)
	if err := g.AddLambdaNode(NodeCompress, compressNode, compose.WithNodeName(NodeCompress)); err != nil {
		panic(fmt.Errorf("add compress: %w", err))
	}

	// --- 节点 5：writer ---
	writerNode := compose.InvokableLambda(
		func(ctx context.Context, _ string) (string, error) {
			var (
				query      string
				role       string
				contextStr string
				sources    []collection.Source
				totalWords int
				onChunk    researcher.ReportChunkFn
			)
			_ = compose.ProcessState[*DeepState](ctx, func(_ context.Context, s *DeepState) error {
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
				s(researcher.Progress{Stage: researcher.StageWriting, Message: "正在撰写深度研究报告……"})
			}
			report, err := researcher.WriteReport(ctx, llmClient, role, query, contextStr, totalWords, sources, onChunk, cfg.Language)
			if err != nil {
				return "", err
			}
			_ = compose.ProcessState[*DeepState](ctx, func(_ context.Context, st *DeepState) error {
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
		{NodePlanSearch, NodeDeepRecurse},
		{NodeDeepRecurse, NodeCompress},
		{NodeCompress, NodeWriter},
		{NodeWriter, compose.END},
	}
	for _, e := range edges {
		if err := g.AddEdge(e[0], e[1]); err != nil {
			panic(fmt.Errorf("edge %s->%s: %w", e[0], e[1], err))
		}
	}
	return g
}

// initialProgress 从 context 取出 OnProgress 回调并包装为 EventFn。
func initialProgress(ctx context.Context) researcher.EventFn {
	st := InitialStateFromContext(ctx)
	if st == nil || st.OnProgress == nil {
		return nil
	}
	return st.OnProgress
}

// progressFromState 与 initialProgress 等价。
func progressFromState(ctx context.Context) researcher.EventFn {
	return initialProgress(ctx)
}
