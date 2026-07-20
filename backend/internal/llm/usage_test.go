package llm

import (
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestUsageCollector_Record(t *testing.T) {
	c := NewUsageCollector()
	c.Record(Usage{Model: "M3", Tier: "fast", Role: "role", Prompt: 100, Completion: 50, Total: 150})
	c.Record(Usage{Model: "M3", Tier: "smart", Role: "writer", Prompt: 200, Completion: 800, Total: 1000, Reasoning: 300})

	if calls := c.Calls(); calls != 2 {
		t.Errorf("Calls = %d, want 2", calls)
	}
	p, comp, total, reason := c.Totals()
	if p != 300 || comp != 850 || total != 1150 || reason != 300 {
		t.Errorf("Totals = %d/%d/%d/%d, want 300/850/1150/300", p, comp, total, reason)
	}
}

func TestUsageCollector_NilSafe(t *testing.T) {
	var c *UsageCollector
	// 所有方法都应 nil 安全，不 panic。
	c.Record(Usage{Total: 100})
	p, comp, total, reason := c.Totals()
	if p != 0 || comp != 0 || total != 0 || reason != 0 {
		t.Errorf("nil collector Totals should be zero, got %d/%d/%d/%d", p, comp, total, reason)
	}
	if c.Calls() != 0 {
		t.Error("nil collector Calls should be 0")
	}
	if c.Summary() != "(未启用计费统计)" {
		t.Errorf("nil Summary = %q", c.Summary())
	}
}

func TestUsageCollector_ZeroTotalSkipped(t *testing.T) {
	c := NewUsageCollector()
	c.Record(Usage{Total: 0}) // 应被跳过
	c.Record(Usage{Prompt: 10, Completion: 5, Total: 15})
	if c.Calls() != 1 {
		t.Errorf("Calls = %d, want 1 (zero-total skipped)", c.Calls())
	}
}

func TestFromResponseMeta_Nil(t *testing.T) {
	u := FromResponseMeta(nil, "model", "fast", "role")
	if u.Total != 0 || u.Model != "model" || u.Tier != "fast" || u.Role != "role" {
		t.Errorf("FromResponseMeta(nil) = %+v", u)
	}

	u = FromResponseMeta(&schema.ResponseMeta{}, "model", "fast", "role")
	if u.Total != 0 {
		t.Errorf("FromResponseMeta(empty meta) Total = %d, want 0", u.Total)
	}
}

func TestFromResponseMeta_WithUsage(t *testing.T) {
	meta := &schema.ResponseMeta{
		Usage: &schema.TokenUsage{
			PromptTokens:                100,
			CompletionTokens:            200,
			TotalTokens:                 300,
			CompletionTokensDetails:     schema.CompletionTokensDetails{ReasoningTokens: 80},
		},
	}
	u := FromResponseMeta(meta, "MiniMax-M3", "smart", "writer")
	if u.Prompt != 100 || u.Completion != 200 || u.Total != 300 || u.Reasoning != 80 {
		t.Errorf("FromResponseMeta = %+v, want prompt=100/completion=200/total=300/reasoning=80", u)
	}
	if u.Model != "MiniMax-M3" || u.Tier != "smart" || u.Role != "writer" {
		t.Errorf("FromResponseMeta business fields = %+v", u)
	}
}

func TestUsageCollector_Summary(t *testing.T) {
	c := NewUsageCollector()
	if s := c.Summary(); s != "(无 LLM 调用)" {
		t.Errorf("empty Summary = %q", s)
	}

	c.Record(Usage{Prompt: 100, Completion: 50, Total: 150})
	s := c.Summary()
	if !strings.Contains(s, "1 次") || !strings.Contains(s, "150 tokens") {
		t.Errorf("Summary without reasoning = %q", s)
	}
	// 不应该出现"思考"（reasoning=0）
	if strings.Contains(s, "思考") {
		t.Errorf("Summary should not mention reasoning when 0: %q", s)
	}

	c.Record(Usage{Prompt: 200, Completion: 800, Total: 1000, Reasoning: 400})
	s = c.Summary()
	if !strings.Contains(s, "思考 400 tokens") || !strings.Contains(s, "47%") {
		t.Errorf("Summary with reasoning = %q", s)
	}
}

func TestUsageCollector_Concurrent(t *testing.T) {
	c := NewUsageCollector()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Record(Usage{Prompt: 1, Completion: 1, Total: 2})
		}()
	}
	wg.Wait()
	if c.Calls() != 100 {
		t.Errorf("Calls = %d, want 100", c.Calls())
	}
	p, _, total, _ := c.Totals()
	if p != 100 || total != 200 {
		t.Errorf("concurrent Totals prompt=%d total=%d, want 100/200", p, total)
	}
}

func TestUsageSnapshot(t *testing.T) {
	c := NewUsageCollector()
	c.Record(Usage{Prompt: 10, Completion: 20, Total: 30, Reasoning: 5})
	snap := c.Snapshot()
	if snap.Calls != 1 || snap.Prompt != 10 || snap.Completion != 20 || snap.Total != 30 || snap.Reasoning != 5 {
		t.Errorf("Snapshot = %+v", snap)
	}
}

func TestUsageCollector_Reset(t *testing.T) {
	c := NewUsageCollector()
	c.Record(Usage{Prompt: 10, Completion: 20, Total: 30})
	c.Reset()
	if c.Calls() != 0 {
		t.Errorf("after Reset Calls = %d, want 0", c.Calls())
	}
	p, _, _, _ := c.Totals()
	if p != 0 {
		t.Errorf("after Reset prompt = %d, want 0", p)
	}
}
