package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestSearcher_RealQuery 对 DuckDuckGo 做一次真实搜索（无需 API Key）。
// 它验证 eino-ext duckduckgo/v2 的 NewSearch 直接调用路径在 z-research 中可用。
// 若网络受限/被限流，允许跳过而非失败。
func TestSearcher_RealQuery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s, err := NewSearcher(ctx)
	if err != nil {
		t.Fatalf("NewSearcher: %v", err)
	}

	results, err := s.Search(ctx, "Golang Eino framework cloudwego", 3)
	if err != nil {
		t.Skipf("跳过：DuckDuckGo 搜索失败（可能是网络/限流）: %v", err)
	}
	if len(results) == 0 {
		t.Skip("跳过：DuckDuckGo 未返回结果（可能是网络/限流）")
	}
	t.Logf("返回 %d 条结果", len(results))
	for i, r := range results {
		t.Logf("  %d. %s\n     %s\n     %s", i+1, r.Title, r.URL, truncate(r.Snippet, 60))
		if strings.TrimSpace(r.URL) == "" {
			t.Errorf("结果 %d 的 URL 为空", i+1)
		}
	}
}

// TestSplitText 验证文本切片的大小与重叠。
func TestSplitText(t *testing.T) {
	text := strings.Repeat("a", 2500) // 2500 字符
	chunks := SplitText(text, 1000, 100)
	if len(chunks) < 2 {
		t.Fatalf("期望至少 2 个块，得到 %d", len(chunks))
	}
	// 第一块应正好 size。
	if len(chunks[0]) != 1000 {
		t.Errorf("第一块长度 = %d，期望 1000", len(chunks[0]))
	}
	// 短文本应原样返回。
	short := "hello"
	if got := SplitText(short, 1000, 100); len(got) != 1 || got[0] != "hello" {
		t.Errorf("短文本处理错误: %v", got)
	}
}

// TestCosine 验证余弦相似度计算。
func TestCosine(t *testing.T) {
	if c := cosine([]float64{1, 0}, []float64{1, 0}); c < 0.999 || c > 1.0001 {
		t.Errorf("相同向量应为 1，得到 %v", c)
	}
	if c := cosine([]float64{1, 0}, []float64{0, 1}); c > 0.0001 || c < -0.0001 {
		t.Errorf("正交向量应为 0，得到 %v", c)
	}
	if c := cosine([]float64{1, 0}, []float64{-1, 0}); c > -0.999 {
		t.Errorf("相反向量应为 -1，得到 %v", c)
	}
}

// TestExtractJSON 验证从含杂质文本中提取 JSON。
func TestExtractJSON(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`["a","b"]`, `["a","b"]`},
		{"```json\n[\"a\"]\n```", `["a"]`},
		{"结果如下：\n[\"x\",\"y\"]\n谢谢", `["x","y"]`},
		{`{"k":1}`, `{"k":1}`},
	}
	for _, c := range cases {
		if got := extractJSON(c.in); got != c.want {
			t.Errorf("extractJSON(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
