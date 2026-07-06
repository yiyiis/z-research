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

	"github.com/joho/godotenv"

	"z-research/backend/internal/api"
	"z-research/backend/internal/config"
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
	dev := flag.Bool("dev", true, "开发模式：启用宽松 CORS（前端 Vite dev server 跨域）")
	flag.Parse()

	cfg, err := config.LoadConfig()
	if err != nil {
		die(err)
	}

	// CLI 模式。
	if *cliQuery != "" {
		runCLI(cfg, *cliQuery)
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

	// 把构造出来的两个引擎传给 NewServer（multi 可能为 nil）。
	singleEngine, multiEngine, err := buildBothEngines(ctx, cfg, st)
	if err != nil {
		die(err)
	}
	srv := api.NewServer(singleEngine, multiEngine, st)
	r := srv.Router(*dev)

	log.Printf("🚀 z-research 后端启动: http://localhost%s", cfg.HTTPAddr)
	if cfg.HTTPProxy != "" {
		log.Printf("   代理: HTTP_PROXY=%s HTTPS_PROXY=%s", cfg.HTTPProxy, cfg.HTTPSProxy)
	}
	if err := r.Run(cfg.HTTPAddr); err != nil {
		die(err)
	}
}

// buildBothEngines 装配单 Agent + 多智能体两个引擎，返回给 router 按需分派。
//
// 单 Agent 引擎总是能构造成功。多智能体引擎如果构造失败
// （例如 CheckPointStore 初始化出错），返回 (single, nil, nil)
// ——前端选 multi 会报错，但 single 仍可用，不会让整个服务
// 起不来。
func buildBothEngines(ctx context.Context, cfg *config.Config, st store.Store) (researcher.EngineIface, researcher.EngineIface, error) {
	// 先设置抓取策略（Jina 宕机时可改 SCRAPER_STRATEGY=direct）。
	scraper.SetStrategy(scraper.ScraperStrategy(cfg.ScraperStrategy))

	llmClient, err := llm.NewLLM(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	searcher, err := search.NewSearcher(ctx)
	if err != nil {
		return nil, nil, err
	}
	r := researcher.NewResearcher(cfg, llmClient, searcher)

	// 单 Agent 总是构造（开销很小）。
	single := researcher.NewEngine(cfg, llmClient, r)

	// 多智能体引擎：尝试构造，失败就退回 single。
	multi, err := buildMultiAgentEngine(ctx, cfg, llmClient, r, st)
	if err != nil {
		log.Printf("⚠️  多智能体引擎构造失败，仅启用 single: %v", err)
		return single, nil, nil
	}
	log.Printf("✅ 多智能体引擎就绪")
	return single, multi, nil
}

func buildMultiAgentEngine(ctx context.Context, cfg *config.Config, llmClient *llm.LLM, inner *researcher.Researcher, st store.Store) (researcher.EngineIface, error) {
	return multiagent.NewEngine(ctx, cfg, llmClient, inner, st)
}

// runCLI 兼容旧用法：直接跑引擎打印报告。
//
// 不设超时（思考模型如 glm-5.1 写报告可能要数分钟），让它跑完。
func runCLI(cfg *config.Config, query string) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	engine, err := buildEngine(ctx, cfg)
	if err != nil {
		die(err)
	}

	report, err := engine.Run(ctx, query, nil, func(p researcher.Progress) {
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
}

// buildEngine 装配研究引擎（LLM + Searcher + Researcher + Engine）。
func buildEngine(ctx context.Context, cfg *config.Config) (*researcher.Engine, error) {
	scraper.SetStrategy(scraper.ScraperStrategy(cfg.ScraperStrategy))

	llmClient, err := llm.NewLLM(ctx, cfg)
	if err != nil {
		return nil, err
	}
	searcher, err := search.NewSearcher(ctx)
	if err != nil {
		return nil, err
	}
	r := researcher.NewResearcher(cfg, llmClient, searcher)
	return researcher.NewEngine(cfg, llmClient, r), nil
}

func die(err error) {
	fmt.Fprintf(os.Stderr, "❌ %v\n", err)
	os.Exit(1)
}
