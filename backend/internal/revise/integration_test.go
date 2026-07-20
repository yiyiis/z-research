// Package revise — integration_test.go 是需要真实 LLM 的集成测试。
//
// 无 ZHIPU_API_KEY 或网络不通时自动 skip(不失败)。
// 用法: ZHIPU_API_KEY=xxx go test -run TestIntegration ./internal/revise/
package revise

import (
	"context"
	"os"
	"testing"
	"time"

	"z-research/backend/internal/config"
	"z-research/backend/internal/llm"
	"z-research/backend/internal/researcher"
	"z-research/backend/internal/search"
)

// skipIfNoLLM 在没有 LLM 凭证或网络不通时跳过测试。
func skipIfNoLLM(t *testing.T) {
	t.Helper()
	if os.Getenv("ZHIPU_API_KEY") == "" {
		t.Skip("跳过: 未设置 ZHIPU_API_KEY(集成测试需要真实 LLM)")
	}
}

// newIntegrationEngine 构造一个真实的 revise.Engine(用于集成测试)。
// 失败时 skip(不 fail,因为可能是网络问题)。
func newIntegrationEngine(t *testing.T) (*Engine, context.Context) {
	t.Helper()
	skipIfNoLLM(t)
	// 加载 .env(从 backend/ 目录)。
	cfg, err := config.LoadConfig()
	if err != nil {
		t.Skipf("跳过: 配置加载失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	llmClient, err := llm.NewLLM(ctx, cfg)
	if err != nil {
		t.Skipf("跳过: LLM 构造失败: %v", err)
	}
	searcher, err := search.NewSearcher(ctx)
	if err != nil {
		t.Skipf("跳过: searcher 构造失败: %v", err)
	}
	r := researcher.NewResearcher(cfg, llmClient, searcher)
	return NewEngine(cfg, llmClient, r, nil), ctx // store=nil(测试不持久化)
}

// TestIntegration_ClassifyInstruction 用真实 LLM 验证分类。
func TestIntegration_ClassifyInstruction(t *testing.T) {
	skipIfNoLLM(t)
	cfg, err := config.LoadConfig()
	if err != nil {
		t.Skipf("配置加载失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	llmClient, err := llm.NewLLM(ctx, cfg)
	if err != nil {
		t.Skipf("LLM 构造失败: %v", err)
	}

	cases := []struct {
		name        string
		instruction string
		wantAction  Action
	}{
		{"补充检索", "补充最新的 MoE 混合专家技术", ActionSupplement},
		{"局部修改", "把结论改简洁一点", ActionLocalEdit},
		{"翻译", "把整篇报告翻译成英文", ActionRestyle},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cls, err := ClassifyInstruction(ctx, llmClient, tc.instruction, "# 测试报告\n正文")
			if err != nil {
				t.Fatalf("ClassifyInstruction: %v", err)
			}
			if cls.Action != tc.wantAction {
				t.Errorf("instruction=%q action=%s, want %s (search_query=%q)",
					tc.instruction, cls.Action, tc.wantAction, cls.SearchQuery)
			}
			if tc.wantAction == ActionSupplement && cls.SearchQuery == "" {
				t.Error("supplement 场景应有 search_query")
			}
		})
	}
}

// TestIntegration_ReviseReport 用真实 LLM 验证流式修改。
func TestIntegration_ReviseReport(t *testing.T) {
	skipIfNoLLM(t)
	cfg, err := config.LoadConfig()
	if err != nil {
		t.Skipf("配置加载失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	llmClient, err := llm.NewLLM(ctx, cfg)
	if err != nil {
		t.Skipf("LLM 构造失败: %v", err)
	}

	original := "# 测试报告\n\n## 第一节\n\n这是正文内容,包含一些论断 [1]。\n\n## 结论\n\n综上所述,这个话题很重要,值得深入研究 [1]。"
	instruction := "把结论改得更简洁(一句话)"

	var chunkCount int
	var lastAccu string
	onChunk := func(chunk, accu string) {
		chunkCount++
		lastAccu = accu
	}

	newReport, err := ReviseReport(ctx, llmClient, original, instruction, "", nil, onChunk, "")
	if err != nil {
		t.Fatalf("ReviseReport: %v", err)
	}
	if newReport == "" {
		t.Fatal("返回空报告")
	}
	if chunkCount == 0 {
		t.Error("应有流式 chunk 回调")
	}
	if lastAccu != newReport {
		t.Error("最后一个 chunk 的 accu 应等于最终报告")
	}
	// 修改后的报告应保留标题结构。
	if len(newReport) < 20 {
		t.Errorf("修改后报告过短: %q", newReport)
	}
}
