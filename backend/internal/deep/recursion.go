// Package deep — recursion.go 实现深度递归的核心逻辑。
//
// deepRecurse 是 deep_recurse Lambda 节点内部调用的递归函数。
// 它不开启新的 Graph（对齐 session 设计"不用 Graph 做递归"），
// 而是用普通 Go 函数递归，每层调用 researcher.Researcher 的资料收集能力。
//
// breadth 衰减策略：每层 breadth = max(2, parentBreadth // 2)
//   - breadth=4 → 下一层 2
//   - breadth=2 → 下一层 2（不再衰减，保底）
//   - breadth=8 → 下一层 4 → 下下层 2
package deep

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"z-research/backend/internal/collection"
	"z-research/backend/internal/llm"
	"z-research/backend/internal/researcher"
	"z-research/backend/internal/workerpool"
)

// followupsPromptSystem/User 是基于上轮 learnings 生成追问查询的 prompt。
// 对齐 session 设计"基于上轮 learnings 追问"。
const (
	followupsSystemPrompt = `你是一个深度研究助手。给定一个原始研究问题和已有的研究要点（learnings），请生成若干个"追问查询"，用于深入研究原始问题中尚未充分覆盖的子主题。

要求：
- 每个追问应聚焦一个原始问题里尚未深挖的具体方面；
- 避免与已有 learnings 重复；
- 使用与原始问题一致的语言；
- 只输出一个 JSON 字符串数组，例如：["追问1", "追问2"]；
- 不要输出任何解释性文字或 markdown 代码块。`

	followupsUserPromptTpl = `原始研究问题：%s

已有的研究要点：
%s

请基于上述信息，生成 %d 个追问查询（JSON 字符串数组）。`
)

// learningsPromptSystem/User 是从一批参考资料提炼 learnings 的 prompt。
const (
	learningsSystemPrompt = `你是一个研究要点提炼助手。给定一批带来源编号的参考资料，请提炼出其中最重要的研究要点（每条一句话）。

要求：
- 每条要点应是一个独立的事实或论断；
- 保留关键数据/数字；
- 使用与资料一致的语言；
- 只输出一个 JSON 字符串数组，例如：["要点1", "要点2"]；
- 不要输出任何解释性文字或 markdown 代码块。`

	learningsUserPromptTpl = `参考资料：
%s

请提炼出最多 %d 条最重要的研究要点（JSON 字符串数组）。`
)

// generateFollowups 用 LLM 基于原始 query + 已有 learnings 生成 N 个追问查询。
// 用 strategic 档位（追问方向决定研究深度，是规划性任务）。
func generateFollowups(ctx context.Context, l *llm.LLM, query string, learnings []string, n int) ([]string, error) {
	if n <= 0 {
		return nil, nil
	}
	learningsStr := "（暂无）"
	if len(learnings) > 0 {
		learningsStr = strings.Join(learnings, "\n- ")
	}
	user := fmt.Sprintf(followupsUserPromptTpl, query, learningsStr, n)
	var out []string
	if err := l.StrategicChatJSON(ctx, followupsSystemPrompt, user, &out); err != nil {
		return nil, fmt.Errorf("生成追问失败: %w", err)
	}
	// 清洗：去空白、去空。
	cleaned := make([]string, 0, len(out))
	for _, s := range out {
		if s = strings.TrimSpace(s); s != "" {
			cleaned = append(cleaned, s)
		}
	}
	return cleaned, nil
}

// extractLearnings 用 LLM 从参考资料文本提炼 learnings。
// 用 fast 档位（结构化提炼，小任务）。
func extractLearnings(ctx context.Context, l *llm.LLM, contextStr string, max int) ([]string, error) {
	if strings.TrimSpace(contextStr) == "" {
		return nil, nil
	}
	if max <= 0 {
		max = 5
	}
	user := fmt.Sprintf(learningsUserPromptTpl, contextStr, max)
	var out []string
	if err := l.FastChatJSON(ctx, learningsSystemPrompt, user, &out); err != nil {
		return nil, fmt.Errorf("提炼 learnings 失败: %w", err)
	}
	cleaned := make([]string, 0, len(out))
	for _, s := range out {
		if s = strings.TrimSpace(s); s != "" {
			cleaned = append(cleaned, s)
		}
	}
	return cleaned, nil
}

// recursionResult 是单层递归的产出。
type recursionResult struct {
	// contextBlocks 是本层及所有子层累积的参考资料文本块。
	contextBlocks []string
	// learnings 是从 contextBlocks 提炼的要点，供父层生成下一轮追问。
	learnings []string
}

// deepRecurse 是深度递归的核心函数。
//
// 参数：
//   - query: 本层要研究的查询（叶子层是具体问题，根层是原始 query）
//   - depth: 剩余递归层数。depth=0 时退化为叶子研究（普通 ConductWithVisited）。
//   - breadth: 本层的扇出数。下一层按 max(2, breadth//2) 衰减。
//   - rootQuery: 原始研究问题，用于生成追问时保持上下文。
//   - visited: 跨层共享的 VisitedSet（面试话术 visited_urls Set）。
//
// 返回本层及所有子层的累积 contextBlocks + learnings。
//
// 并发控制：本层的 breadth 个子查询用 workerpool 并发执行（受
// cfg.MaxScraperWorkers 全局上限约束）。
func deepRecurse(
	ctx context.Context,
	r *researcher.Researcher,
	l *llm.LLM,
	query string,
	depth, breadth int,
	rootQuery string,
	visited *collection.VisitedSet,
	onProgress researcher.EventFn,
) (*recursionResult, error) {
	emit := func(stage researcher.Stage, msg string) {
		if onProgress != nil {
			onProgress(researcher.Progress{Stage: stage, Message: msg})
		}
	}

	// 叶子层：直接调 ConductWithVisited 收集资料。
	if depth <= 0 {
		emit(researcher.StageSearching, fmt.Sprintf("叶子研究: %s (breadth=%d)", truncate(query, 40), breadth))
		res, err := r.ConductWithVisited(ctx, query, breadth, visited, onProgress)
		if err != nil {
			return nil, err
		}
		learnings, _ := extractLearnings(ctx, l, res.Context, 5)
		return &recursionResult{
			contextBlocks: []string{res.Context},
			learnings:     learnings,
		}, nil
	}

	// 非叶子层：先收集本层资料，再生成追问，递归下一层。
	emit(researcher.StageSearching, fmt.Sprintf("深度递归 layer: %s (depth=%d, breadth=%d)", truncate(query, 40), depth, breadth))

	// 1. 本层资料收集（用 breadth 作为子查询数）。
	res, err := r.ConductWithVisited(ctx, query, breadth, visited, onProgress)
	if err != nil {
		return nil, err
	}
	ownLearnings, _ := extractLearnings(ctx, l, res.Context, 5)

	// 2. 生成下一层追问查询（基于原始 rootQuery + 本层 learnings）。
	nextBreadth := nextLayerBreadth(breadth)
	followups, err := generateFollowups(ctx, l, rootQuery, ownLearnings, nextBreadth)
	if err != nil || len(followups) == 0 {
		// 追问生成失败不致命，退化为只用本层资料。
		return &recursionResult{
			contextBlocks: []string{res.Context},
			learnings:     ownLearnings,
		}, nil
	}

	// 3. 并发递归下一层（每个追问一个子树）。
	// 用 workerpool 控制并发，避免 breadth 过大时 goroutine 爆炸。
	pool := workerpool.New(breadth)
	var (
		mu     sync.Mutex
		blocks []string
		allLrn []string
		firstErr error
	)
	for _, fq := range followups {
		fq := fq
		pool.Go(ctx, func() error {
			sub, err := deepRecurse(ctx, r, l, fq, depth-1, nextBreadth, rootQuery, visited, onProgress)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return nil // 单个子树失败不中断整体（保守记录首个错误）
			}
			mu.Lock()
			blocks = append(blocks, sub.contextBlocks...)
			allLrn = append(allLrn, sub.learnings...)
			mu.Unlock()
			return nil
		})
	}
	_ = pool.Wait()

	if firstErr != nil {
		// 若有子树失败，仍返回本层资料（部分结果优于全失败）。
		emit(researcher.StageSearching, fmt.Sprintf("部分子树失败: %v", firstErr))
	}

	// 合并：本层资料 + 所有子层资料。
	allBlocks := append([]string{res.Context}, blocks...)
	allLearnings := append(append([]string{}, ownLearnings...), allLrn...)
	return &recursionResult{
		contextBlocks: allBlocks,
		learnings:     allLearnings,
	}, nil
}

// nextLayerBreadth 计算下一层的 breadth：max(2, breadth//2)。
// 这是 session 明确的衰减策略。
func nextLayerBreadth(breadth int) int {
	half := breadth / 2
	if half < 2 {
		return 2
	}
	return half
}

// truncate 把字符串截断到 maxRune 字符（用于进度日志）。
func truncate(s string, maxRune int) string {
	r := []rune(s)
	if len(r) <= maxRune {
		return s
	}
	return string(r[:maxRune]) + "…"
}
