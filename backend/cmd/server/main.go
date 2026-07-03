// Package main 是 z-research 后端入口（Gin HTTP 服务）。
//
// 启动 HTTP 服务（默认 :8080），提供：
//
//	POST   /api/research      SSE 流式研究
//	GET    /api/reports        历史列表
//	GET    /api/reports/:id    单篇报告
//	DELETE /api/reports/:id    删除报告
//	GET    /*                  内嵌的 SPA 前端（生产）
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

	"z-research/backend/internal/api"
	"z-research/backend/internal/config"
	"z-research/backend/internal/llm"
	"z-research/backend/internal/researcher"
	"z-research/backend/internal/search"
	"z-research/backend/internal/store"
)

func main() {
	// 兼容旧 CLI 用法：--cli "查询" 直接打印报告到 stdout。
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

	st, err := store.New(ctx, cfg.DBPath)
	if err != nil {
		die(err)
	}
	defer st.Close()

	engine, err := buildEngine(ctx, cfg)
	if err != nil {
		die(err)
	}

	srv := api.NewServer(engine, st)
	r := srv.Router(*dev)

	log.Printf("🚀 z-research 后端启动: http://localhost%s", cfg.HTTPAddr)
	if err := r.Run(cfg.HTTPAddr); err != nil {
		die(err)
	}
}

// buildEngine 装配研究引擎（LLM + Searcher + Researcher + Engine）。
func buildEngine(ctx context.Context, cfg *config.Config) (*researcher.Engine, error) {
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

// runCLI 兼容旧用法：直接跑引擎打印报告。
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

func die(err error) {
	fmt.Fprintf(os.Stderr, "❌ %v\n", err)
	os.Exit(1)
}
