package main

import (
	"context"
	"os"
	"testing"

	openaiembed "github.com/cloudwego/eino-ext/components/embedding/openai"
)

// TestOpenAIEmbedder_OllamaCompat 验证 eino-ext 的 OpenAI 兼容 embedder
// 能直接对接自建 Ollama 的 /v1/embeddings 接口（这是简化后的唯一 embedding 路径）。
//
// 通过环境变量 OLLAMA_TEST_BASE（默认 http://43.138.247.132:11434）启用，未连上则跳过。
func TestOpenAIEmbedder_OllamaCompat(t *testing.T) {
	base := os.Getenv("OLLAMA_TEST_BASE")
	if base == "" {
		base = "http://43.138.247.132:11434"
	}
	model := os.Getenv("OLLAMA_TEST_MODEL")
	if model == "" {
		model = "nomic-embed-text"
	}

	ctx := context.Background()
	// 注意 BaseURL 末尾带 /v1，走 Ollama 的 OpenAI 兼容层；APIKey 填任意非空值。
	emb, err := openaiembed.NewEmbedder(ctx, &openaiembed.EmbeddingConfig{
		BaseURL: base + "/v1",
		APIKey:  "ollama",
		Model:   model,
	})
	if err != nil {
		t.Fatalf("NewEmbedder: %v", err)
	}

	// 批量 embed（eino-ext 会把多个文本作为数组一次性发送，验证 Ollama 兼容层支持批量）。
	vecs, err := emb.EmbedStrings(ctx, []string{"你好世界", "hello world", "固态电池"})
	if err != nil {
		t.Skipf("跳过：无法连接 Ollama (%s): %v", base, err)
	}
	if len(vecs) != 3 {
		t.Fatalf("期望 3 个向量，得到 %d", len(vecs))
	}
	if len(vecs[0]) == 0 {
		t.Fatal("向量维度为 0")
	}
	t.Logf("✅ OpenAI 兼容 embedder 对接 Ollama 成功：3 条文本，每条 %d 维", len(vecs[0]))

	same := cosine(vecs[0], vecs[0])
	if same < 0.99 {
		t.Errorf("自相似度应≈1，得到 %v", same)
	}
	t.Logf("余弦相似度: 自身=%.3f, 中英(hello)=%.3f, 中英(电池)=%.3f",
		same, cosine(vecs[0], vecs[1]), cosine(vecs[0], vecs[2]))
}
