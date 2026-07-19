// Package researcher — state.go 定义单 Agent 5 节点 Graph 的
// 共享运行时状态。
//
// Graph 拓扑（对齐面试话术里的"单 Agent 5 节点线性 Graph"）：
//
//	START → choose_role → plan_search → parallel_research → compression → writer → END
//
// Graph 的 I/O 类型是 string（最终报告正文），ResearchState 由
// compose.WithGenLocalState 创建的 per-run 槽位承载，节点通过
// compose.ProcessState 读写。所有字段 last-write-wins。
package researcher

import (
	"z-research/backend/internal/collection"
)

// ResearchState 是单 Agent Graph 的 per-run 共享状态。
type ResearchState struct {
	// Query 是用户原始研究问题，一次设置后不再修改。
	Query string

	// Role 是 choose_role 节点产出的领域专家角色设定（中文 prompt 片段）。
	Role string

	// SubQueries 是 plan_search 节点拆出的子查询列表。
	SubQueries []string

	// Visited 是 parallel_research 节点使用的跨子查询共享 VisitedSet。
	// 用指针是为了在 graph 状态里持有同一个 Set 实例（map 不能直接复制共享）。
	Visited *collection.VisitedSet

	// ContextBlock 是 parallel_research 节点产出的"带来源编号的参考资料
	// 文本块"的拼接结果，作为 writer 节点的 LLM 输入。
	Context string

	// Sources 是 VisitedSet 累积的来源列表（parallel_research 写入）。
	Sources []collection.Source

	// Report 是 writer 节点写回的最终 Markdown 报告。
	Report string

	// TotalWords 是报告目标字数，可选覆盖 cfg.TotalWords。
	TotalWords int

	// ---- per-run 注入的回调（不进 graph 编译期）----
	OnProgress     EventFn
	OnReportChunk  ReportChunkFn
}
