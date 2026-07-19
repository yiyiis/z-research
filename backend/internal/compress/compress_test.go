package compress

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/embedding"
)

// TestSplitText 验证文本切片的大小与重叠。
func TestSplitText(t *testing.T) {
	text := strings.Repeat("a", 2500) // 2500 字符
	chunks := SplitText(text, 1000, 100)
	if len(chunks) < 2 {
		t.Fatalf("期望至少 2 个块，得到 %d", len(chunks))
	}
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
	if c := Cosine([]float64{1, 0}, []float64{1, 0}); c < 0.999 || c > 1.0001 {
		t.Errorf("相同向量应为 1，得到 %v", c)
	}
	if c := Cosine([]float64{1, 0}, []float64{0, 1}); c > 0.0001 || c < -0.0001 {
		t.Errorf("正交向量应为 0，得到 %v", c)
	}
	if c := Cosine([]float64{1, 0}, []float64{-1, 0}); c > -0.999 {
		t.Errorf("相反向量应为 -1，得到 %v", c)
	}
	// 维度不匹配应返回 0。
	if c := Cosine([]float64{1, 0}, []float64{1}); c != 0 {
		t.Errorf("维度不匹配应返回 0，得到 %v", c)
	}
}

// TestCompress_FastPath 验证小文档快路径：内容低于阈值时原样返回，不调用 embedding。
func TestCompress_FastPath(t *testing.T) {
	// 用一个永远不该被调用的 embedder；若被调用则测试失败。
	emb := &panickingEmbedder{}
	out, err := Compress(nil, emb, "query", "短文本", 0.42, 5, 8000, 4)
	if err != nil {
		t.Fatalf("快路径不应出错: %v", err)
	}
	if out != "短文本" {
		t.Errorf("快路径应原样返回，得到 %q", out)
	}
}

// TestCompress_EmptyText 空文本直接返回空。
func TestCompress_EmptyText(t *testing.T) {
	out, err := Compress(nil, nil, "q", "", 0.42, 5, 0, 4)
	if err != nil || out != "" {
		t.Errorf("空文本应返回 (\"\", nil)，得到 (%q, %v)", out, err)
	}
}

// panickingEmbedder 一旦被调用就 panic，用于验证快路径不触发 embedding。
type panickingEmbedder struct{}

func (p *panickingEmbedder) EmbedStrings(_ context.Context, _ []string, _ ...embedding.Option) ([][]float64, error) {
	panic("快路径不应调用 embedding")
}
