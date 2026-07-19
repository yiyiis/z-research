package researcher

import (
	"context"
	"testing"

	"z-research/backend/internal/config"
	"z-research/backend/internal/llm"
)

// TestBuildSingleGraph_Compiles 验证单 Agent 5 节点 Graph 能成功装配 + 编译。
// 这是 smoke 级测试：不真正跑 LLM/网络，只验证 graph 拓扑合法、Eino 能接受。
func TestBuildSingleGraph_Compiles(t *testing.T) {
	cfg := &config.Config{
		MaxIterations:        2,
		MaxResultsPerQuery:   2,
		MaxScrapePerQuery:    1,
		SimilarityThreshold:  0.42,
		CompressionThreshold: 8000,
		TotalWords:           800,
		Language:             "zh",
		Concurrency:          2,
		MaxScraperWorkers:    5,
		MaxEmbedWorkers:      2,
		MaxRunSteps:          30,
	}
	// 用零值 LLM/Researcher：本测试不 Invoke，只验证 Compile。
	llmClient := &llm.LLM{}
	r := &Researcher{cfg: cfg, llm: llmClient}

	g := BuildSingleGraph(context.Background(), cfg, llmClient, r)
	if g == nil {
		t.Fatal("BuildSingleGraph returned nil")
	}
	// Compile 会校验节点/边/分支拓扑合法性。
	runnable, err := g.Compile(context.Background())
	if err != nil {
		t.Fatalf("compile single-agent graph: %v", err)
	}
	if runnable == nil {
		t.Fatal("compiled runnable is nil")
	}
}

// TestSingleGraphNodeKeys 验证节点 key 常量稳定（graph 拓扑的契约）。
func TestSingleGraphNodeKeys(t *testing.T) {
	want := map[string]string{
		NodeChooseRole:       "choose_role",
		NodePlanSearch:       "plan_search",
		NodeParallelResearch: "parallel_research",
		NodeCompression:      "compression",
		NodeWriter:           "writer",
	}
	for k, want := range want {
		if k != want {
			t.Errorf("node key mismatch: const %q != want %q", k, want)
		}
	}
}
