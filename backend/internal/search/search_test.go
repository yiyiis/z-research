package search

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestSearcher_RealQuery 对 DuckDuckGo 做一次真实搜索（无需 API Key）。
// 它验证 NewSearcher 的直接调用路径可用。若网络受限/被限流，允许跳过而非失败。
func TestSearcher_RealQuery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s, err := NewSearcher(ctx)
	if err != nil {
		t.Fatalf("NewSearcher: %v", err)
	}

	results, err := s.Search(ctx, "cloudwego eino golang framework", 3)
	if err != nil {
		t.Skipf("跳过：DuckDuckGo 搜索失败（可能是网络/限流）: %v", err)
	}
	if len(results) == 0 {
		t.Skip("跳过：DuckDuckGo 未返回结果（可能是网络/限流）")
	}
	t.Logf("返回 %d 条结果", len(results))
	for i, r := range results {
		t.Logf("  %d. %s\n     %s\n     %s", i+1, r.Title, r.URL, shortStr(r.Snippet, 60))
		if strings.TrimSpace(r.URL) == "" {
			t.Errorf("结果 %d 的 URL 为空", i+1)
		}
	}
}

// TestSearch_EmptyQuery 空查询应返回 nil，不发起请求。
func TestSearch_EmptyQuery(t *testing.T) {
	ctx := context.Background()
	s, err := NewSearcher(ctx)
	if err != nil {
		t.Fatalf("NewSearcher: %v", err)
	}
	got, err := s.Search(ctx, "   ", 5)
	if err != nil {
		t.Errorf("空查询不应返回错误: %v", err)
	}
	if got != nil {
		t.Errorf("空查询应返回 nil，得到 %v", got)
	}
}

func shortStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
