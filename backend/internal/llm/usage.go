// Package llm — usage.go 实现 LLM 调用的 token 用量统计（流量计费）。
//
// 数据来源：Eino 的 model.Generate 返回的 *schema.Message.ResponseMeta.Usage。
// 包含 PromptTokens / CompletionTokens / TotalTokens / ReasoningTokens。
// 其中 ReasoningTokens 是思考模型（MiniMax-M3 / o1 等）的思考 token，
// 这是计费里容易被忽略但成本占比很高的部分。
//
// 用法：LLM 内部持有 *UsageCollector，每次 chatWith/chatJSONWith/streamWith
// 调用后从 resp.ResponseMeta 读出 usage，调用 collector.Record。
// 上层（CLI/handlers/engine）通过 LLM.Usage() 拿到 collector 做汇总展示。
package llm

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/cloudwego/eino/schema"
)

// Usage 记录单次 LLM 调用的 token 用量。
type Usage struct {
	Model      string // 模型名（如 "MiniMax-M3"）
	Tier       string // 档位："fast" / "smart" / "strategic"
	Role       string // 调用方角色（如 "planner" / "writer" / "choose_role"，空表示未标注）
	Prompt     int    // 输入 token
	Completion int    // 输出 token
	Total      int    // 合计 token
	Reasoning  int    // 思考 token（思考模型专属，普通模型为 0）
}

// UsageCollector 是并发安全的 token 用量累加器。
//
// 一次研究流程内所有 LLM 调用（可能几十次：选角色 + 规划 + 每节检索 +
// 多轮 reviewer/reviser + writer + fact_checker...）的 usage 都累加到这里。
// 研究结束时调用 Summary 拿到人类可读的成本汇总。
type UsageCollector struct {
	mu     sync.Mutex
	runs   []Usage      // 每次调用的明细（供按角色/档位聚合）
	prompt atomic.Int64 // 累计 prompt token
	compl  atomic.Int64 // 累计 completion token
	total  atomic.Int64 // 累计 total token
	reason atomic.Int64 // 累计 reasoning token（思考模型专属）
}

// NewUsageCollector 创建一个空的 collector。
func NewUsageCollector() *UsageCollector {
	return &UsageCollector{}
}

// Record 记录一次 LLM 调用的用量。
//
// 入参 u.Model/Tier/Role 用于明细聚合；Prompt/Completion/Total/Reasoning
// 累加到对应计数器。nil 安全（u.Total=0 时直接返回）。
func (c *UsageCollector) Record(u Usage) {
	if c == nil || u.Total == 0 {
		return
	}
	c.mu.Lock()
	c.runs = append(c.runs, u)
	c.mu.Unlock()
	c.prompt.Add(int64(u.Prompt))
	c.compl.Add(int64(u.Completion))
	c.total.Add(int64(u.Total))
	c.reason.Add(int64(u.Reasoning))
}

// FromResponseMeta 从 Eino 的 *schema.ResponseMeta 提取 Usage。
//
// resp 为 nil 或 resp.Usage 为 nil 时返回零值 Usage（不报错，部分流式调用拿不到）。
// tier/role/model 由调用方传入（ResponseMeta 里没有这些业务信息）。
func FromResponseMeta(resp *schema.ResponseMeta, model, tier, role string) Usage {
	if resp == nil || resp.Usage == nil {
		return Usage{Model: model, Tier: tier, Role: role}
	}
	u := resp.Usage
	return Usage{
		Model:      model,
		Tier:       tier,
		Role:       role,
		Prompt:     u.PromptTokens,
		Completion: u.CompletionTokens,
		Total:      u.TotalTokens,
		Reasoning:  u.CompletionTokensDetails.ReasoningTokens,
	}
}

// Totals 返回累计的 (prompt, completion, total, reasoning)。
func (c *UsageCollector) Totals() (prompt, completion, total, reasoning int64) {
	if c == nil {
		return 0, 0, 0, 0
	}
	return c.prompt.Load(), c.compl.Load(), c.total.Load(), c.reason.Load()
}

// Runs 返回每次调用的明细副本（按记录顺序）。
func (c *UsageCollector) Runs() []Usage {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Usage, len(c.runs))
	copy(out, c.runs)
	return out
}

// Calls 返回累计调用次数。
func (c *UsageCollector) Calls() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.runs)
}

// Summary 返回人类可读的 token 用量汇总（多行字符串）。
//
// 用于 CLI 结束时打印、WebSocket done 帧附带、日志记录。
// 思考模型的 reasoning 单独列出（成本占比高，值得让用户看见）。
func (c *UsageCollector) Summary() string {
	if c == nil {
		return "(未启用计费统计)"
	}
	prompt, completion, total, reasoning := c.Totals()
	calls := c.Calls()
	if calls == 0 {
		return "(无 LLM 调用)"
	}
	s := fmt.Sprintf("LLM 调用 %d 次 | 输入 %d / 输出 %d / 合计 %d tokens",
		calls, prompt, completion, total)
	if reasoning > 0 && completion > 0 {
		// 思考模型：reasoning_tokens 是输出 token 的一部分，
		// 但成本上通常按 completion 计费，单独标注便于评估思考开销。
		s += fmt.Sprintf("（其中思考 %d tokens，占输出的 %.0f%%）",
			reasoning, float64(reasoning)/float64(completion)*100)
	}
	return s
}

// Reset 清空所有计数（用于复用 collector，一般不需要）。
func (c *UsageCollector) Reset() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.runs = nil
	c.mu.Unlock()
	c.prompt.Store(0)
	c.compl.Store(0)
	c.total.Store(0)
	c.reason.Store(0)
}

// UsageSnapshot 是给 FinalReport 用的快照（JSON 友好）。
// collector 本身有锁不适合直接 JSON 序列化，快照是只读副本。
type UsageSnapshot struct {
	Calls      int   `json:"calls"`
	Prompt     int64 `json:"prompt_tokens"`
	Completion int64 `json:"completion_tokens"`
	Total      int64 `json:"total_tokens"`
	Reasoning  int64 `json:"reasoning_tokens,omitempty"`
}

// Snapshot 返回当前的只读快照。
func (c *UsageCollector) Snapshot() UsageSnapshot {
	if c == nil {
		return UsageSnapshot{}
	}
	p, comp, t, r := c.Totals()
	return UsageSnapshot{
		Calls:      c.Calls(),
		Prompt:     p,
		Completion: comp,
		Total:      t,
		Reasoning:  r,
	}
}
