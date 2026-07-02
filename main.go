package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// z-research: 一个基于 Eino 的简易研究 Agent（Go 版 gpt-researcher 默认工作流）。
//
// 用法：
//
//	export ZHIPU_API_KEY=xxx   # 或写入 .env
//	go run . "你的研究问题"
//
// 流程：choose_agent → plan 子查询 → 并发{search→fetch→compress} → 撰写中文 Markdown 报告。
func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "用法: go run . \"<研究问题>\"")
		fmt.Fprintln(os.Stderr, "示例: go run . \"2026 年固态电池降本的最新进展\"")
		os.Exit(2)
	}
	query := strings.Join(os.Args[1:], " ")

	cfg, err := LoadConfig()
	if err != nil {
		die(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	logf("🚀 z-research 启动")
	logf("   查询: %s", query)
	logf("   模型: %s (LLM) / %s (Embedding)", cfg.LLMModel, cfg.EmbedModel)
	logf("   子查询数=%d，每查询搜索=%d 抓取=%d，并发=%d",
		cfg.MaxIterations, cfg.MaxResultsPerQuery, cfg.MaxScrapePerQuery, cfg.Concurrency)

	llm, err := NewLLM(ctx, cfg)
	if err != nil {
		die(err)
	}

	search, err := NewSearcher(ctx)
	if err != nil {
		die(err)
	}

	// ---- 阶段 1：确定专家角色 ----
	role, err := chooseAgent(ctx, llm, query)
	if err != nil {
		// 角色生成失败不致命，退化为通用研究员。
		logf("⚠ 角色生成失败，使用默认角色: %v", err)
		role = "你是一名严谨的研究助理，擅长基于资料客观地撰写研究报告。"
	}
	logf("🧠 角色: %s", oneLine(role))

	// ---- 阶段 2：收集资料（研究工作流）----
	researcher := NewResearcher(cfg, llm, search)
	contextStr, sources, err := researcher.Conduct(ctx, query)
	if err != nil {
		die(err)
	}

	// ---- 阶段 3：撰写最终报告 ----
	logf("✍️  正在撰写报告……")
	report, err := llm.Chat(ctx,
		reportSystemPrompt(role, cfg.Language),
		reportUserPrompt(query, contextStr, cfg.TotalWords),
	)
	if err != nil {
		die(fmt.Errorf("撰写报告失败: %w", err))
	}

	// 若报告中缺少"参考资料"段，补一份来源清单（保证可溯源）。
	if !strings.Contains(report, "参考资料") && len(sources) > 0 {
		report = strings.TrimRight(report, "\n") + "\n\n## 参考资料\n"
		for _, s := range sources {
			report += fmt.Sprintf("%d. %s — %s\n", s.N, s.Title, s.URL)
		}
	}

	// 输出到 stdout 并落盘到 outputs/。
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println(report)
	fmt.Println(strings.Repeat("=", 60))

	if err := saveReport(query, report); err != nil {
		logf("⚠ 报告保存失败: %v", err)
	}
	logf("🎉 完成！")
}

// chooseAgent 让 LLM 给出角色设定文本（纯文本，非 JSON）。
func chooseAgent(ctx context.Context, llm *LLM, query string) (string, error) {
	role, err := llm.Chat(ctx, chooseAgentSystemPrompt(), chooseAgentUserPrompt(query))
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(role) == "" {
		return "", fmt.Errorf("空角色")
	}
	return role, nil
}

// saveReport 把报告以时间戳命名写入 outputs/ 目录，返回文件路径。
func saveReport(query, report string) error {
	if err := os.MkdirAll("outputs", 0o755); err != nil {
		return err
	}
	name := fmt.Sprintf("report-%s.md", time.Now().Format("20060102-150405"))
	path := filepath.Join("outputs", name)
	// 文件头加一个注释，记录原始查询。
	content := fmt.Sprintf("<!-- query: %s -->\n\n%s", query, report)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return err
	}
	logf("💾 报告已保存: %s", path)
	return nil
}

func die(err error) {
	fmt.Fprintf(os.Stderr, "❌ %v\n", err)
	os.Exit(1)
}

// oneLine 把多行文本压成一行，便于日志展示。
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", "")
	return strings.TrimSpace(s)
}
