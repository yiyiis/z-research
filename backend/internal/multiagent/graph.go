// Package multiagent — graph.go builds the outer Eino
// compose.Graph that orchestrates the multi-agent research
// pipeline.
//
// Topology:
//
//	START → planner → human_review ──(accept)──→ researcher → writer → END
//	            ▲           │
//	            └──(revise)─┘
//
// planner:   LLM emits a research outline (title + sections).
// human_review: when EnableHITL is true, blocks until the
//
//	caller (API layer) supplies a HumanFeedbackFn
//	callback. When false, auto-accepts.
//
// researcher: fans out to per-section subgraphs (one per
//
//	section in the outline). Each subgraph runs the
//	sec_researcher → reviewer ↔ reviser loop
//	implemented in draft_graph.go.
//
// writer:    streams the final markdown report by combining
//
//	all per-section drafts.
//
// The graph's I/O type is string (the final report). The
// ResearchState is held in the per-run local state slot
// (compose.WithGenLocalState) and read/written via
// compose.ProcessState from each node.
package multiagent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/compose"

	"z-research/backend/internal/config"
	"z-research/backend/internal/llm"
	"z-research/backend/internal/researcher"
)

// Node keys (also referenced by draft_graph.go when
// registering the inner section subgraphs as GraphNodes).
const (
	NodeBrowser    = "browser"
	NodePlanner    = "planner"
	NodeHumanRev   = "human_review"
	NodeResearcher = "researcher"
	NodeWriter     = "writer"

	// 第三个循环：事实核查 + 可视化 + 发布。
	// 仅当 cfg.EnableFactCheck=true 时 writer 之后才会接 fact_checker；
	// fact_checker 通过则路由到 visualizer（若 EnableVisualize）或 publisher。
	NodeFactChecker = "fact_checker"
	NodeVisualizer  = "visualizer"
	NodePublisher   = "publisher"

	// Branch targets. Used both as graph node keys and
	// as the strings returned by branch conditions.
	BranchAccept = NodeResearcher // human_review accept → researcher
	BranchRevise = NodePlanner    // human_review revise → planner

	// Fact-check branch targets.
	FactBranchAccept = NodeVisualizer // fact_checker pass → visualizer
	FactBranchRevise = NodeWriter     // fact_checker fail → writer 重写
)

// defaultReviewerGuidelines is the per-section review
// criteria. Mirrors gpt-researcher multi_agents/task.json
// guidelines format. Used by draft_graph.go's reviewer
// node until per-task guidelines are exposed via config.
var defaultReviewerGuidelines = []string{
	"分节草稿必须基于给定的参考资料，不引入外部知识。",
	"每个关键论断必须用 [n] 引用支撑，引用紧跟论断之后。",
	"段落之间有逻辑递进或对比，避免无序堆砌。",
	"避免同义重复，不要在不同段落用不同措辞表达同一观点。",
	"全文同一概念使用同一术语。",
	"数据/事实优先使用具体数字，避免'约''大致'等模糊词。",
}

// injectInitialStatePreHandler is the shared pre-handler
// attached to BOTH the Browser node and the Planner node.
// It copies per-run values (query, EnableHITL,
// HumanFeedbackFn, onProgress, onReportChunk) from the
// context (set by Engine.Run via WithInitialState) into
// the graph's per-run *ResearchState.
//
// Why both nodes? The graph runs START → Browser → Planner.
// If we only attached this to Planner, the Browser node
// (which runs first) would see an empty state.Query and
// fail with "research topic is empty". Attaching to both
// is idempotent — the second invocation is a no-op because
// the fields are already set.
var injectInitialStatePreHandler = compose.StatePreHandler[string, *ResearchState](
	func(ctx context.Context, in string, state *ResearchState) (string, error) {
		initial := InitialStateFromContext(ctx)
		if initial == nil || state == nil {
			return in, nil
		}
		if state.Query == "" {
			state.Query = initial.Query
		}
		if state.MaxSections == 0 {
			state.MaxSections = initial.MaxSections
		}
		if state.MaxPlanRevisions == 0 {
			state.MaxPlanRevisions = initial.MaxPlanRevisions
		}
		state.EnableHITL = initial.EnableHITL
		state.HumanFeedbackFn = initial.HumanFeedbackFn
		state.OnReportChunk = initial.OnReportChunk
		state.OnProgress = initial.OnProgress
		return in, nil
	},
)

// BuildOuterGraph assembles (but does not compile) the outer
// multi-agent graph. The returned graph is intended to be
// compiled once per server start (Compile is expensive).
//
// deps wires in:
//   - llm: the shared LLM client (chat + embed)
//   - inner: the researcher.Researcher reused for per-section
//     material gathering (the existing single-agent
//     implementation, called as a function — not a subgraph)
//   - cfg:  thresholds and limits
//   - opts: per-run overrides (HumanFeedbackFn, TaskID)
//
// The HumanFeedbackFn is read from opts at run time
// (researcher.Engine.Run passes it to graph.Invoke via the
// Compose ProcessState, see engine.go). When opts is nil or
// the callback is nil, the graph auto-accepts every plan.
func BuildOuterGraph(
	ctx context.Context,
	cfg *config.Config,
	llmClient *llm.LLM,
	inner *researcher.Researcher,
) *compose.Graph[string, string] {
	g := compose.NewGraph[string, string](
		compose.WithGenLocalState(func(ctx context.Context) *ResearchState {
			return &ResearchState{}
		}),
	)

	// --- Browser node (initial research pass) ---
	// Mirrors gpt-researcher multi_agents orchestrator's
	// "browser" node: it runs a full single-agent research
	// pass on the original query and produces a summary of
	// what sources exist. The Planner then uses this
	// summary to make a much more informed outline (vs.
	// planning from just the raw query).
	//
	// IMPORTANT: Browser runs FIRST (START→Browser→Planner),
	// so the per-run state injection (query, EnableHITL,
	// HumanFeedbackFn...) must happen HERE, not only on
	// Planner. We attach the same injectInitialState
	// pre-handler to both nodes.
	browserNode := compose.InvokableLambda(
		func(ctx context.Context, _ string) (string, error) {
			var query string
			_ = compose.ProcessState[*ResearchState](ctx, func(_ context.Context, s *ResearchState) error {
				query = s.Query
				return nil
			})
			summary, err := BrowserResearch(ctx, inner, query)
			if err != nil {
				return "", err
			}
			_ = compose.ProcessState[*ResearchState](ctx, func(_ context.Context, s *ResearchState) error {
				s.InitialResearch = summary
				return nil
			})
			// Forward the Browser's initial_research
			// summary to the WebSocket so the user can
			// see what the multi-agent Browser found
			// before being asked to review the outline.
			// The frontend renders this as a Markdown
			// block above the HumanFeedbackPanel.
			if s := OnProgressFromContext(ctx); s != nil {
				s("browser", summary)
			}
			return summary, nil
		},
	)
	if err := g.AddLambdaNode(NodeBrowser, browserNode,
		compose.WithNodeName(NodeBrowser),
		compose.WithStatePreHandler(injectInitialStatePreHandler),
	); err != nil {
		panic(fmt.Errorf("add browser: %w", err))
	}

	// --- Planner node ---
	plannerNode := compose.InvokableLambda(
		func(ctx context.Context, _ string) (string, error) {
			var (
				query           string
				maxSec          int
				initialResearch string
				humanFeedback   string
			)
			_ = compose.ProcessState[*ResearchState](ctx, func(_ context.Context, s *ResearchState) error {
				query = s.Query
				maxSec = s.MaxSections
				initialResearch = s.InitialResearch
				humanFeedback = s.HumanFeedback
				return nil
			})
			if maxSec <= 0 {
				maxSec = cfg.MaxSections
			}
			// Mirrors gpt-researcher: Planner receives
			// the Browser's initial_research summary +
			// (optional) human feedback from a prior
			// revise round.
			title, sections, err := PlanOutline(ctx, llmClient, query, initialResearch, humanFeedback, maxSec)
			if err != nil {
				return "", err
			}
			_ = compose.ProcessState[*ResearchState](ctx, func(_ context.Context, s *ResearchState) error {
				s.Title = title
				s.Sections = sections
				return nil
			})
			// The graph's I/O is string; we pass the
			// title as the "data" the human_review node
			// receives. The actual sections list is in
			// the per-run state.
			return title, nil
		},
	)
	// planner pre-handler: copy per-run initial state
	// from the context (set by Engine.Run) into the
	// graph's per-run *ResearchState. This is the
	// mechanism for per-run injection of query, HITL
	// callback, onProgress, etc. — see ctx.go.
	// The SAME handler is attached to Browser (which runs
	// first) so it also sees the injected query.
	if err := g.AddLambdaNode(NodePlanner, plannerNode,
		compose.WithNodeName(NodePlanner),
		compose.WithStatePreHandler(injectInitialStatePreHandler),
	); err != nil {
		panic(fmt.Errorf("add planner: %w", err))
	}

	// --- Human review node (with conditional branch) ---
	humanNode := compose.InvokableLambda(
		func(ctx context.Context, in string) (string, error) {
			// Read current state.
			var (
				title        string
				sections     []string
				revisions    int
				feedbackFn   researcher.HumanFeedbackFn
				enableHITL   bool
				maxRevisions int
			)
			_ = compose.ProcessState[*ResearchState](ctx, func(_ context.Context, s *ResearchState) error {
				title = s.Title
				sections = s.Sections
				revisions = s.PlanRevisions
				feedbackFn = s.HumanFeedbackFn
				enableHITL = s.EnableHITL
				maxRevisions = s.MaxPlanRevisions
				return nil
			})

			// Auto-accept: no HITL, no callback, or
			// revision cap reached.
			if !enableHITL || feedbackFn == nil || revisions >= maxRevisions {
				_ = compose.ProcessState[*ResearchState](ctx, func(_ context.Context, s *ResearchState) error {
					s.HumanFeedback = ""
					return nil
				})
				return acceptSentinel, nil
			}

			// Block on the human. The callback honors
			// ctx cancellation, so a client disconnect
			// will abort the run cleanly.
			feedback, err := feedbackFn(ctx, researcher.HumanReviewPlan{
				Title:    title,
				Sections: sections,
				Revision: revisions,
			})
			if err != nil {
				return "", fmt.Errorf("human feedback: %w", err)
			}
			_ = compose.ProcessState[*ResearchState](ctx, func(_ context.Context, s *ResearchState) error {
				s.HumanFeedback = feedback
				if feedback == "" {
					return nil
				}
				s.PlanRevisions++
				return nil
			})
			if feedback == "" {
				return acceptSentinel, nil
			}
			return reviseSentinel, nil
		},
	)
	if err := g.AddLambdaNode(NodeHumanRev, humanNode, compose.WithNodeName(NodeHumanRev)); err != nil {
		panic(fmt.Errorf("add human_review: %w", err))
	}

	// --- Researcher node (fans out to per-section subgraphs) ---
	// Implementation lives in researcher_node.go.
	researcherNode, err := buildResearcherNode(cfg, llmClient, inner)
	if err != nil {
		panic(fmt.Errorf("build researcher node: %w", err))
	}
	if err := g.AddLambdaNode(NodeResearcher, researcherNode, compose.WithNodeName(NodeResearcher)); err != nil {
		panic(fmt.Errorf("add researcher: %w", err))
	}

	// --- Writer node ---
	// Mirrors gpt-researcher writer.py::run: the Writer
	// generates a JSON layout (table_of_contents /
	// introduction / conclusion / sources) and the engine
	// assembles the final markdown around the per-section
	// drafts. Streaming is per-token via the LLM's
	// ChatStream channel; we forward each chunk through
	// the onReportChunk callback so the WebSocket gets
	// progressive output.
	writerNode := compose.InvokableLambda(
		func(ctx context.Context, _ string) (string, error) {
			var (
				query    string
				title    string
				sections []string
				drafts   []string
				onChunk  func(string, string)
			)
			_ = compose.ProcessState[*ResearchState](ctx, func(_ context.Context, s *ResearchState) error {
				query = s.Query
				title = s.Title
				sections = s.Sections
				drafts = s.Drafts
				onChunk = s.OnReportChunk
				return nil
			})
			// Serialize the per-section drafts into a
			// single research_data blob (what
			// gpt-researcher feeds the Writer as
			// "Research data: {data}").
			type draftEntry struct {
				Section string `json:"section"`
				Draft   string `json:"draft"`
			}
			entries := make([]draftEntry, 0, len(sections))
			for i, s := range sections {
				if i < len(drafts) {
					entries = append(entries, draftEntry{Section: s, Draft: drafts[i]})
				}
			}
			researchData, _ := json.Marshal(entries)

			// Streaming: forward a "report_chunk" frame
			// for each assembled section, mimicking the
			// single-agent onReportChunk pattern.
			// We don't stream the LLM's raw tokens here
			// because the Writer output is a JSON layout
			// (per gpt-researcher) — we stream the
			// assembled markdown after the layout is
			// generated.
			layout, err := WriteReportLayout(ctx, llmClient, query, title, string(researchData))
			if err != nil {
				return "", err
			}
			report := AssembleReport(title, layout, sections, drafts)
			// Stream the assembled report in one
			// chunk (the WebSocket handler can
			// still render it as "received").
			if onChunk != nil {
				onChunk(report, report)
			}
			_ = compose.ProcessState[*ResearchState](ctx, func(_ context.Context, s *ResearchState) error {
				s.Report = report
				return nil
			})
			return report, nil
		},
	)
	if err := g.AddLambdaNode(NodeWriter, writerNode, compose.WithNodeName(NodeWriter)); err != nil {
		panic(fmt.Errorf("add writer: %w", err))
	}

	// --- Fact checker node (第三个循环，受 cfg.EnableFactCheck 开关控制) ---
	// 对齐 session 设计：fact_checker 只看报告正文（intro+data+conclusion），
	// 不看 URL 不看引用。核查通过路由到 visualizer，不通过路由回 writer 重写。
	// 上限 MaxFactCheckRevisions，到达后强制通过（避免死循环）。
	// 注意：此节点仅在 EnableFactCheck=true 时被接入图拓扑（见下方边装配）。
	factCheckerNode := compose.InvokableLambda(
		func(ctx context.Context, _ string) (string, error) {
			var (
				report     string
				rounds     int
				maxRounds  int
			)
			_ = compose.ProcessState[*ResearchState](ctx, func(_ context.Context, s *ResearchState) error {
				report = s.Report
				rounds = s.FactCheckRounds
				maxRounds = cfg.MaxFactCheckRevisions
				return nil
			})
			if maxRounds <= 0 {
				maxRounds = 2
			}
			// 上限保护：到达后强制通过。
			if rounds >= maxRounds {
				if s := OnProgressFromContext(ctx); s != nil {
					s("fact_checker", fmt.Sprintf("已达事实核查上限 %d 轮，强制通过", maxRounds))
				}
				return factPassSentinel, nil
			}
			if s := OnProgressFromContext(ctx); s != nil {
				s("fact_checker", fmt.Sprintf("正在进行第 %d 轮事实核查", rounds+1))
			}
			result, err := FactCheck(ctx, llmClient, report)
			if err != nil {
				return "", err
			}
			// 写回核查报告与轮数。
			_ = compose.ProcessState[*ResearchState](ctx, func(_ context.Context, st *ResearchState) error {
				st.FactCheckReport = result.Report
				if result.Verdict != "pass" {
					st.FactCheckRounds = rounds + 1
				}
				return nil
			})
			if result.Verdict == "pass" {
				if s := OnProgressFromContext(ctx); s != nil {
					s("fact_checker", "事实核查通过")
				}
				return factPassSentinel, nil
			}
			if s := OnProgressFromContext(ctx); s != nil {
				s("fact_checker", "事实核查未通过，回 writer 重写")
			}
			// 返回非 sentinel 的任意字符串（核查报告）→ 路由回 writer。
			// 把核查报告作为传递给 writer 的 data，writer 会读 state 重写。
			return result.Report, nil
		},
	)
	if err := g.AddLambdaNode(NodeFactChecker, factCheckerNode, compose.WithNodeName(NodeFactChecker)); err != nil {
		panic(fmt.Errorf("add fact_checker: %w", err))
	}

	// --- Visualizer node (受 cfg.EnableVisualize 开关控制) ---
	// 接收核查通过的报告，生成元数据 + 可选 mermaid 概览。
	visualizerNode := compose.InvokableLambda(
		func(ctx context.Context, _ string) (string, error) {
			var (
				title  string
				report string
			)
			_ = compose.ProcessState[*ResearchState](ctx, func(_ context.Context, s *ResearchState) error {
				title = s.Title
				report = s.Report
				return nil
			})
			if s := OnProgressFromContext(ctx); s != nil {
				s("visualizer", "正在生成报告可视化概览")
			}
			visuals, err := Visualize(ctx, llmClient, title, report)
			if err != nil {
				return "", err
			}
			_ = compose.ProcessState[*ResearchState](ctx, func(_ context.Context, st *ResearchState) error {
				st.Visuals = visuals
				return nil
			})
			return visuals, nil
		},
	)
	if err := g.AddLambdaNode(NodeVisualizer, visualizerNode, compose.WithNodeName(NodeVisualizer)); err != nil {
		panic(fmt.Errorf("add visualizer: %w", err))
	}

	// --- Publisher node (终节点，组装最终输出) ---
	// 把 report + 核查摘要 + 可视化元数据 + sources 拼成最终输出。
	// 同时解决 Sources 未回传 FinalReport 的 TODO（通过 state 传递）。
	publisherNode := compose.InvokableLambda(
		func(ctx context.Context, _ string) (string, error) {
			var (
				report      string
				factReport  string
				visuals     string
				sources     []researcher.Source
			)
			_ = compose.ProcessState[*ResearchState](ctx, func(_ context.Context, s *ResearchState) error {
				report = s.Report
				factReport = s.FactCheckReport
				visuals = s.Visuals
				sources = s.Sources
				return nil
			})
			final := Publish(report, factReport, visuals, sources)
			_ = compose.ProcessState[*ResearchState](ctx, func(_ context.Context, st *ResearchState) error {
				st.Report = final
				return nil
			})
			return final, nil
		},
	)
	if err := g.AddLambdaNode(NodePublisher, publisherNode, compose.WithNodeName(NodePublisher)); err != nil {
		panic(fmt.Errorf("add publisher: %w", err))
	}

	// --- Branch on human: accept→researcher, revise→planner ---
	// MUST be added after researcher + writer nodes are
	// registered because Eino's AddBranch validates that
	// every endNode in the endNodes map is already a graph
	// node. (See compose/graph.go:516 and graph_smoke_test.go
	// for the constraint.)
	humanBranch := compose.NewGraphBranch(
		func(_ context.Context, in string) (string, error) {
			if in == acceptSentinel {
				return BranchAccept, nil
			}
			return BranchRevise, nil
		},
		map[string]bool{BranchAccept: true, BranchRevise: true},
	)
	if err := g.AddBranch(NodeHumanRev, humanBranch); err != nil {
		panic(fmt.Errorf("add human branch: %w", err))
	}

	// --- Edges ---
	// START → Browser → Planner → Human → [accept: Researcher |
	// revise: Planner] → Writer → END. Mirrors gpt-researcher
	// multi_agents orchestrator.
	if err := g.AddEdge(compose.START, NodeBrowser); err != nil {
		panic(fmt.Errorf("edge START->browser: %w", err))
	}
	if err := g.AddEdge(NodeBrowser, NodePlanner); err != nil {
		panic(fmt.Errorf("edge browser->planner: %w", err))
	}
	if err := g.AddEdge(NodePlanner, NodeHumanRev); err != nil {
		panic(fmt.Errorf("edge planner->human: %w", err))
	}
	// human -> researcher is established by the branch's
	// "accept" arm. The "revise" arm cycles back to
	// planner. Do NOT also AddEdge here — see smoke test
	// and graph_smoke_test.go for why (data would flow
	// on both paths).
	if err := g.AddEdge(NodeResearcher, NodeWriter); err != nil {
		panic(fmt.Errorf("edge researcher->writer: %w", err))
	}

	// ---- 第三个循环的边装配（受开关控制） ----
	// 模式 A（默认，EnableFactCheck=false）: writer → END（保持旧行为）
	// 模式 B（EnableFactCheck=true, EnableVisualize=false）: writer → fact_checker → [pass: publisher / fail: writer] → END
	// 模式 C（EnableFactCheck=true, EnableVisualize=true）: writer → fact_checker → [pass: visualizer / fail: writer] → publisher → END
	if cfg.EnableFactCheck {
		if err := g.AddEdge(NodeWriter, NodeFactChecker); err != nil {
			panic(fmt.Errorf("edge writer->fact_checker: %w", err))
		}
		// fact-check 分支：pass → 下游(visualizer 或 publisher)，fail → writer。
		var acceptTarget string
		if cfg.EnableVisualize {
			acceptTarget = NodeVisualizer
		} else {
			acceptTarget = NodePublisher
		}
		factBranch := compose.NewGraphBranch(
			func(_ context.Context, in string) (string, error) {
				if in == factPassSentinel {
					return acceptTarget, nil
				}
				return NodeWriter, nil
			},
			map[string]bool{acceptTarget: true, NodeWriter: true},
		)
		if err := g.AddBranch(NodeFactChecker, factBranch); err != nil {
			panic(fmt.Errorf("add fact_check branch: %w", err))
		}
		// visualizer → publisher（若启用），否则 accept arm 直接是 publisher。
		if cfg.EnableVisualize {
			if err := g.AddEdge(NodeVisualizer, NodePublisher); err != nil {
				panic(fmt.Errorf("edge visualizer->publisher: %w", err))
			}
		}
		// publisher 是终点。
		if err := g.AddEdge(NodePublisher, compose.END); err != nil {
			panic(fmt.Errorf("edge publisher->END: %w", err))
		}
	} else {
		// 模式 A：保持现状，writer 直接是终点。
		if err := g.AddEdge(NodeWriter, compose.END); err != nil {
			panic(fmt.Errorf("edge writer->END: %w", err))
		}
	}

	return g
}
