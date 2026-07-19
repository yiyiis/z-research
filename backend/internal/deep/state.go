// Package deep 实现深度递归研究引擎——Eino compose.Graph 编排的
// "单 Agent / 多 Agent / 深度递归" 三种研究模式中的第三种。
//
// 拓扑（对齐 session 设计 "Lambda 节点内递归，复用单 Agent 图，不开新 Graph 做递归"）：
//
//	START → choose_role → plan_search → deep_recurse(Lambda,递归) → compress → writer → END
//
// 与单 Agent 的关键区别：
//   - deep_recurse 是一个 Lambda 节点，节点内部用普通 Go 函数递归（不是 graph 嵌套）
//   - 每层 breadth 按 max(2, breadth//2) 衰减，控制递归树规模
//   - 跨层共享 collection.VisitedSet，避免重复抓取
//   - 基于上轮 learnings 生成下一层追问查询
//
// 递归结构（depth=2, breadth=4 为例）：
//
//	layer 0: query (breadth=4)
//	  ├─ layer 1: followup_1 (breadth=max(2,4//2)=2)
//	  │   ├─ layer 2: followup_1_1 (breadth=2, leaf)
//	  │   └─ layer 2: followup_1_2 (breadth=2, leaf)
//	  ├─ layer 1: followup_2 (breadth=2)
//	  ...
//
// 成本随 breadth^depth 增长，默认 (4,2) 较温和。
package deep

import (
	"z-research/backend/internal/collection"
	"z-research/backend/internal/researcher"
)

// DeepState 是深度递归 Graph 的 per-run 共享状态。
type DeepState struct {
	// Query 原始研究问题。
	Query string

	// Role 是 choose_role 节点产出的领域专家角色设定。
	Role string

	// InitialFollowups 是 plan_search 节点从原始 query 拆出的第一层追问查询。
	InitialFollowups []string

	// Visited 是跨递归层共享的 URL 集合（面试话术 visited_urls Set）。
	// 用指针是为了在递归调用间共享同一个 Set 实例。
	Visited *collection.VisitedSet

	// Context 是 deep_recurse 节点累积的所有叶子研究的参考资料拼接。
	Context string

	// Sources 是 Visited 累积的来源列表。
	Sources []collection.Source

	// Report 是 writer 节点写回的最终报告。
	Report string

	// ---- per-run 配置 ----
	Breadth    int // 起始 breadth（0 → cfg.DeepBreadth）
	Depth      int // 递归层数（0 → cfg.DeepDepth）
	TotalWords int // 报告目标字数

	// ---- per-run 回调 ----
	OnProgress    researcher.EventFn
	OnReportChunk researcher.ReportChunkFn
}
