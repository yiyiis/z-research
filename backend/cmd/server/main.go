// Package main 是 z-research 后端入口（Gin HTTP 服务）。
//
// 启动 HTTP 服务（默认 :8080），提供：
//
//	GET    /ws              —— WebSocket 研究（实时推送进度 + 流式报告）
//	GET    /api/reports     —— 历史列表
//	GET    /api/reports/:id —— 单篇报告
//	DELETE /api/reports/:id —— 删除报告
//	GET    /*               —— 内嵌的 SPA 前端（生产）
//
// 也兼容旧的 CLI 用法：传入 --cli "<query>" 时直接跑引擎并把报告打印到 stdout。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"z-research/backend/internal/agent"
	"z-research/backend/internal/api"
	"z-research/backend/internal/config"
	"z-research/backend/internal/deep"
	"z-research/backend/internal/eval"
	"z-research/backend/internal/llm"
	"z-research/backend/internal/multiagent"
	"z-research/backend/internal/researcher"
	"z-research/backend/internal/scraper"
	"z-research/backend/internal/search"
	"z-research/backend/internal/store"
)

// proxyEnvVars godotenv 加载到进程但 Go 的 net/http 只认系统
// 环境变量。Go 的 os.Environ() 在 godotenv.Load 之后才生效；
// 而 http.Transport 读的是 os.Getenv。我们把 .env 里的代理变量
// 同步到 os.Setenv，确保所有后续 http 调用都能走代理。
func syncProxyEnv() {
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		if v := os.Getenv(key); v != "" {
			continue
		}
	}
}

// main 启动 HTTP 服务（或 --cli 模式）。
func main() {
	// 先加载 .env 到 os 进程环境，godotenv 不覆盖系统已有变量。
	// 这步必须在任何 config / http 调用之前。
	_ = godotenv.Load()

	cliQuery := flag.String("cli", "", "CLI 模式：直接研究并打印报告到 stdout（不启动 HTTP 服务）")
	cliMode := flag.String("mode", "single", "CLI 模式引擎：single / multi / react / deep")
	cliBreadth := flag.Int("breadth", 0, "CLI deep 模式 breadth（0=用 cfg.DeepBreadth）")
	cliDepth := flag.Int("depth", 0, "CLI deep 模式 depth（0=用 cfg.DeepDepth）")
	dev := flag.Bool("dev", true, "开发模式：启用宽松 CORS（前端 Vite dev server 跨域）")
	flag.Parse()

	cfg, err := config.LoadConfig()
	if err != nil {
		die(err)
	}

	// CLI 模式。
	if *cliQuery != "" {
		runCLI(cfg, *cliQuery, *cliMode, *cliBreadth, *cliDepth)
		return
	}

	// HTTP 服务模式。
	ctx := context.Background()

	// 关键：把 cfg 里的代理配置同步到系统环境变量，
	// Go 的 http.Transport 才能用到（即使 .env 已 Load，
	// godotenv 设到进程环境但 http 库读 os.Getenv，需同步）。
	if cfg.HTTPProxy != "" {
		os.Setenv("HTTP_PROXY", cfg.HTTPProxy)
		os.Setenv("HTTPS_PROXY", cfg.HTTPSProxy)
	}

	st, err := store.New(ctx, cfg.DBPath)
	if err != nil {
		die(err)
	}
	defer st.Close()

	// 把构造出来的四个引擎传给 NewServer（multi/react/deep 可能为 nil）。
	singleEngine, multiEngine, reactEngine, deepEngine, llmClient, err := buildBothEngines(ctx, cfg, st)
	if err != nil {
		die(err)
	}
	// 构造评估 store（LLM-as-Judge 评估结果持久化）。失败非致命，降级为不评估。
	var evalStore *store.SQLiteEvaluationStore
	if es, esErr := store.NewSQLiteEvaluationStore(ctx, st); esErr == nil {
		evalStore = es
	} else {
		log.Printf("⚠️  评估 store 构造失败，将不启用自动评估: %v", esErr)
	}
	srv := api.NewServer(singleEngine, multiEngine, reactEngine, deepEngine, st, llmClient, evalStore, cfg.EvalOnDone)
	r := srv.Router(*dev)

	log.Printf("🚀 z-research 后端启动: http://localhost%s", cfg.HTTPAddr)
	if cfg.HTTPProxy != "" {
		log.Printf("   代理: HTTP_PROXY=%s HTTPS_PROXY=%s", cfg.HTTPProxy, cfg.HTTPSProxy)
	}
	if err := r.Run(cfg.HTTPAddr); err != nil {
		die(err)
	}
}

// buildBothEngines 装配 single + multi + react + deep 四个引擎，返回给 router 按需分派。
//
// single 总是构造成功。multi / react / deep 若构造失败返回 nil（前端选对应模式时报错），
// 但 single 仍可用，不会让整个服务起不来。
// 同时返回共享的 llmClient（供 handlers 拿 token 用量统计）。
func buildBothEngines(ctx context.Context, cfg *config.Config, st store.Store) (researcher.EngineIface, researcher.EngineIface, researcher.EngineIface, researcher.EngineIface, *llm.LLM, error) {
	// 先设置抓取策略（Jina 宕机时可改 SCRAPER_STRATEGY=direct）。
	scraper.SetStrategy(scraper.ScraperStrategy(cfg.ScraperStrategy))

	llmClient, err := llm.NewLLM(ctx, cfg)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	searcher, err := search.NewSearcher(ctx)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	r := researcher.NewResearcher(cfg, llmClient, searcher)

	// 单 Agent 总是构造（开销很小）。
	single := researcher.NewEngine(cfg, llmClient, r)

	// 多智能体引擎：尝试构造，失败就退回 single。
	multi, err := buildMultiAgentEngine(ctx, cfg, llmClient, r, st)
	if err != nil {
		log.Printf("⚠️  多智能体引擎构造失败，仅启用 single: %v", err)
		multi = nil
	} else {
		log.Printf("✅ 多智能体引擎就绪")
	}

	// ReAct Agent 引擎：尝试构造，失败就退回 single。
	reactEng, err := buildAgentEngine(ctx, cfg, llmClient, searcher)
	if err != nil {
		log.Printf("⚠️  ReAct Agent 引擎构造失败: %v", err)
		reactEng = nil
	} else {
		log.Printf("✅ ReAct Agent 引擎就绪")
	}

	// 深度递归引擎：尝试构造，失败就退回 single。
	deepEng, err := buildDeepEngine(ctx, cfg, llmClient, r)
	if err != nil {
		log.Printf("⚠️  深度递归引擎构造失败: %v", err)
		deepEng = nil
	} else {
		log.Printf("✅ 深度递归引擎就绪 (breadth=%d, depth=%d)", cfg.DeepBreadth, cfg.DeepDepth)
	}

	return single, multi, reactEng, deepEng, llmClient, nil
}

func buildMultiAgentEngine(ctx context.Context, cfg *config.Config, llmClient *llm.LLM, inner *researcher.Researcher, st store.Store) (researcher.EngineIface, error) {
	return multiagent.NewEngine(ctx, cfg, llmClient, inner, st)
}

// buildAgentEngine 构造 ReAct Agent 引擎（LLM 自主调用搜索/抓取工具）。
func buildAgentEngine(ctx context.Context, cfg *config.Config, llmClient *llm.LLM, searcher *search.Searcher) (researcher.EngineIface, error) {
	return agent.NewAgentEngine(ctx, cfg, llmClient, searcher)
}

// buildDeepEngine 构造深度递归引擎（Lambda 节点内递归，breadth 逐层衰减）。
func buildDeepEngine(ctx context.Context, cfg *config.Config, llmClient *llm.LLM, inner *researcher.Researcher) (researcher.EngineIface, error) {
	return deep.NewEngine(cfg, llmClient, inner), nil
}

// runCLI 兼容旧用法：直接跑引擎打印报告。
//
// 不设超时（思考模型如 glm-5.1 写报告可能要数分钟），让它跑完。
//
// mode: "single"（默认）/ "multi" / "react" / "deep"。
// breadth/depth: 仅 deep 模式生效，0 = 用 cfg 默认值。
func runCLI(cfg *config.Config, query, mode string, breadth, depth int) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	scraper.SetStrategy(scraper.ScraperStrategy(cfg.ScraperStrategy))
	llmClient, err := llm.NewLLM(ctx, cfg)
	if err != nil {
		die(err)
	}
	searcher, err := search.NewSearcher(ctx)
	if err != nil {
		die(err)
	}
	r := researcher.NewResearcher(cfg, llmClient, searcher)

	// 按 mode 选引擎。
	var engine researcher.EngineIface
	switch mode {
	case "deep":
		engine = deep.NewEngine(cfg, llmClient, r)
		fmt.Fprintf(os.Stderr, "🚀 深度递归引擎 (breadth=%d, depth=%d)\n", orDefault(breadth, cfg.DeepBreadth), orDefault(depth, cfg.DeepDepth))
	case "multi":
		m, e := multiagent.NewEngine(ctx, cfg, llmClient, r, nil)
		if e != nil {
			die(e)
		}
		engine = m
		fmt.Fprintln(os.Stderr, "🚀 多智能体引擎")
	case "react":
		a, e := agent.NewAgentEngine(ctx, cfg, llmClient, searcher)
		if e != nil {
			die(e)
		}
		engine = a
		fmt.Fprintln(os.Stderr, "🚀 ReAct Agent 引擎")
	default:
		engine = researcher.NewEngine(cfg, llmClient, r)
		fmt.Fprintln(os.Stderr, "🚀 单 Agent 引擎")
	}

	// 构造 opts（仅 deep 模式传 breadth/depth）。
	var opts *researcher.Options
	if mode == "deep" {
		opts = &researcher.Options{}
		if breadth > 0 {
			opts.Breadth = &breadth
		}
		if depth > 0 {
			opts.Depth = &depth
		}
	}

	report, err := engine.Run(ctx, query, opts, func(p researcher.Progress) {
		msg := p.Message
		if msg == "" {
			msg = fmt.Sprintf("stage=%s subquery=%s url=%s found=%d", p.Stage, p.SubQuery, p.URL, p.Found)
		}
		fmt.Fprintf(os.Stderr, "🔍 [%s] %s\n", p.Stage, msg)
	}, nil)
	if err != nil {
		die(err)
	}
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println(report.Markdown)
	fmt.Println(strings.Repeat("=", 60))
	// 流量计费汇总（CLI 模式打印到 stderr，不污染报告 stdout）。
	if u := llmClient.Usage(); u != nil {
		fmt.Fprintf(os.Stderr, "\n📊 %s\n", u.Summary())
	}
	// LLM-as-Judge 自动评估（与 WS handler 一致的逻辑）。
	// 评估失败不致命，只记日志。
	if cfg.EvalOnDone {
		runCLIEvaluation(ctx, llmClient, query, report.Markdown, report.Sources)
	}
}

// runCLIEvaluation 在 CLI 模式下给报告打分并打印到 stderr。
func runCLIEvaluation(ctx context.Context, l *llm.LLM, query, reportMarkdown string, sources []researcher.Source) {
	rows := make([]eval.SourceRow, len(sources))
	for i, src := range sources {
		rows[i] = eval.SourceRow{N: src.N, URL: src.URL, Title: src.Title}
	}
	evalCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	score, err := eval.JudgeReport(evalCtx, l, query, reportMarkdown, rows)
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠️  评估失败: %v\n", err)
		return
	}
	dto := score.ToDTO()
	fmt.Fprintf(os.Stderr, "\n📝 评估: 综合 %.1f/10 — %s\n", dto.Overall, dto.Summary)
	for _, d := range dto.Dimensions {
		fmt.Fprintf(os.Stderr, "   %-12s %.1f  %s\n", d.Label, d.Score, d.Note)
	}
}

// orDefault 返回 v（若 >0）否则 def。
func orDefault(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

func die(err error) {
	fmt.Fprintf(os.Stderr, "❌ %v\n", err)
	os.Exit(1)
}
