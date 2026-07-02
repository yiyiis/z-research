package main

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/cloudwego/eino/components/embedding"
)

// chunkSize / chunkOverlap 对齐 gpt-researcher 的 RecursiveCharacterTextSplitter 默认值。
const (
	chunkSize    = 1000
	chunkOverlap = 100
)

// scoredChunk 用于按相似度排序文档块。
type scoredChunk struct {
	text  string
	score float64
}

// SplitText 把长文本切成带重叠的块（字符级滑动窗口）。
// size 为每块字符数，overlap 为相邻块重叠字符数。
func SplitText(text string, size, overlap int) []string {
	if size <= 0 {
		size = chunkSize
	}
	if overlap < 0 || overlap >= size {
		overlap = chunkOverlap
	}
	r := []rune(text)
	if len(r) == 0 {
		return nil
	}
	if len(r) <= size {
		return []string{string(r)}
	}

	step := size - overlap
	if step <= 0 {
		step = 1
	}
	var chunks []string
	for i := 0; i < len(r); i += step {
		end := i + size
		if end > len(r) {
			end = len(r)
		}
		chunks = append(chunks, string(r[i:end]))
		if end == len(r) {
			break
		}
	}
	return chunks
}

// Compress 对一篇网页的正文做"即用即抛"的内存 embedding 过滤。
//
// 流程（对齐 .md 文档中"纯内存 Embedding 压缩"方案，不依赖任何外部向量库）：
//  1. 切分为带重叠的小块；
//  2. 批量计算小块与查询的 embedding；
//  3. 按余弦相似度排序，保留 ≥ threshold 的块，最多取 topK 个；
//  4. 将保留的块按原文顺序拼接返回。
//
// 当总字符数 < compressionThreshold 时直接返回原文（快路径，避免无谓的 embedding 调用）。
func Compress(
	ctx context.Context,
	emb embedding.Embedder,
	query, text string,
	threshold float64,
	topK int,
	compressionThreshold int,
) (string, error) {
	if text == "" {
		return "", nil
	}
	// 快路径：内容本身就不长，无需压缩。
	if compressionThreshold > 0 && len([]rune(text)) <= compressionThreshold {
		return text, nil
	}

	chunks := SplitText(text, chunkSize, chunkOverlap)
	if len(chunks) == 0 {
		return "", nil
	}

	// 一次性 embed：[query, chunk0, chunk1, ...]
	all := make([]string, 0, len(chunks)+1)
	all = append(all, query)
	all = append(all, chunks...)
	vecs, err := emb.EmbedStrings(ctx, all)
	if err != nil {
		return "", fmt.Errorf("计算 embedding 失败: %w", err)
	}
	if len(vecs) != len(all) {
		return "", fmt.Errorf("embedding 返回数量不匹配: got=%d want=%d", len(vecs), len(all))
	}
	queryVec := vecs[0]

	scored := make([]scoredChunk, 0, len(chunks))
	for i, chunkVec := range vecs[1:] {
		s := cosine(queryVec, chunkVec)
		if s >= threshold {
			scored = append(scored, scoredChunk{text: chunks[i], score: s})
		}
	}
	if len(scored) == 0 {
		return "", nil
	}

	// 保留相似度最高的 topK 个。
	sort.Slice(scored, func(i, j int) bool { return scored[i].score > scored[j].score })
	if topK > 0 && len(scored) > topK {
		scored = scored[:topK]
	}

	// 为了保留行文连贯性，按"在原文中的出现顺序"重新拼接。
	// 通过遍历原始 chunks 顺序重建。
	keep := make(map[string]bool, len(scored))
	for _, sc := range scored {
		keep[sc.text] = true
	}
	var b []byte
	for _, c := range chunks {
		if keep[c] {
			b = append(b, c...)
			b = append(b, '\n')
		}
	}
	return string(b), nil
}

// cosine 计算两个向量的余弦相似度。
func cosine(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
