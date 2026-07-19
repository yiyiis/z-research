package deep

import (
	"context"
	"testing"

	"z-research/backend/internal/config"
	"z-research/backend/internal/llm"
	"z-research/backend/internal/researcher"
)

// TestNextLayerBreadth 验证 session 设计的 breadth 衰减策略：max(2, breadth//2)。
func TestNextLayerBreadth(t *testing.T) {
	cases := []struct {
		input int
		want  int
	}{
		{8, 4}, // 8 → 4
		{4, 2}, // 4 → 2
		{3, 2}, // 3 → 1 < 2，保底 2
		{2, 2}, // 2 → 1 < 2，保底 2（不再衰减）
		{1, 2}, // 1 → 0 < 2，保底 2
		{0, 2}, // 0 → 保底 2
	}
	for _, tc := range cases {
		got := nextLayerBreadth(tc.input)
		if got != tc.want {
			t.Errorf("nextLayerBreadth(%d) = %d, want %d", tc.input, got, tc.want)
		}
	}
}

// TestBuildDeepGraph_Compiles 验证深度递归 5 节点 Graph 能编译。
func TestBuildDeepGraph_Compiles(t *testing.T) {
	cfg := &config.Config{
		DeepBreadth:    4,
		DeepDepth:      2,
		TotalWords:     800,
		Language:       "zh",
		MaxRunSteps:    80,
	}
	// 用零值 LLM/Researcher：本测试不 Invoke，只验证 Compile。
	llmClient := &llm.LLM{}
	r := &researcher.Researcher{}

	g := BuildDeepGraph(context.Background(), cfg, llmClient, r)
	if g == nil {
		t.Fatal("BuildDeepGraph returned nil")
	}
	runnable, err := g.Compile(context.Background())
	if err != nil {
		t.Fatalf("compile deep graph: %v", err)
	}
	if runnable == nil {
		t.Fatal("compiled runnable is nil")
	}
}

// TestDeepGraphNodeKeys 验证节点 key 常量稳定。
func TestDeepGraphNodeKeys(t *testing.T) {
	want := map[string]string{
		NodeChooseRole:  "choose_role",
		NodePlanSearch:  "plan_search",
		NodeDeepRecurse: "deep_recurse",
		NodeCompress:    "compress",
		NodeWriter:      "writer",
	}
	for k, want := range want {
		if k != want {
			t.Errorf("node key mismatch: const %q != want %q", k, want)
		}
	}
}

// TestEngine_ImplementsEngineIface 编译期断言：*Engine 实现 researcher.EngineIface。
// （var _ 已在 engine.go 中，这里再加一个测试级的确认。）
func TestEngine_ImplementsEngineIface(t *testing.T) {
	var _ researcher.EngineIface = (*Engine)(nil)
}

// TestTruncate 验证进度日志用的字符串截断。
func TestTruncate(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("truncate(short) = %q, want %q", got, "hello")
	}
	if got := truncate("hello world", 5); got != "hello…" {
		t.Errorf("truncate(long) = %q, want %q", got, "hello…")
	}
}
