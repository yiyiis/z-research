// Package api 实现 z-research 的 HTTP 层（Gin + WebSocket）。
//
// 路由：
//
//	GET    /ws              —— WebSocket 研究端点（全双工，实时推送进度/报告）
//	GET    /api/reports     —— 历史报告列表
//	GET    /api/reports/:id —— 单篇报告全文
//	DELETE /api/reports/:id —— 删除报告
//	GET    /*               —— 内嵌的 SPA 前端（生产）
package api

// ResearchRequest 是 WebSocket 客户端发来的研究请求。
//
// Mode: "single"（默认，单 Agent）或 "multi"（多智能体
// 状态图）或 "react"（ReAct Agent）或 "deep"（深度递归）。
// 空字符串 = 走服务端配置 ENGINE_MODE。
//
// HitL: 多智能体模式下启用 Human-in-the-loop 大纲审核。
// 开启后 Browser 节点跑完会立即把 initial_research
// 摘要推给前端；Planner 完成后会阻塞等待用户对
// 大纲的 accept/revise 回复。关闭时（默认 false）
// 所有阶段自动 accept。
//
// TaskID: 多智能体模式下的可恢复检查点 ID。前端每次
// 研究请求生成一个 UUID，服务端用它持久化 state；
// 中断后用同 TaskID 重启可从断点续跑（实验性，
// 阶段 5 完成时启用）。
//
// Breadth/Depth: 仅深度递归模式（mode="deep"）生效。
// 控制递归起始扇出与层数。nil = 用 cfg.DeepBreadth/DeepDepth。
type ResearchRequest struct {
	Query  string `json:"query"`
	Mode   string `json:"mode,omitempty"`
	HitL   bool   `json:"hitl,omitempty"`
	TaskID string `json:"task_id,omitempty"`

	// ReportType 仅对单 Agent 模式（mode="single" 或空）生效：
	//   "brief"（默认，简报）或 "detailed"（详细，多轮拆分长报告）。
	// 多智能体模式忽略此字段（它本身就是详细报告流程）。
	ReportType string `json:"report_type,omitempty"`

	// 深度递归模式（mode="deep"）的 per-run 参数。指针类型以区分"未传"和"零值"。
	Breadth *int `json:"breadth,omitempty"`
	Depth   *int `json:"depth,omitempty"`
}

// HumanFeedbackResponse 是 WebSocket 客户端对服务端
// human_feedback 帧的回复。
//
// Accept=true 表示接受当前大纲（无 notes 字段）。Accept=false
// 表示拒绝，Notes 给出修改意见（注入 multiagent 引擎
// 的 HumanFeedbackFn → 驱动 revise 回路）。
type HumanFeedbackResponse struct {
	Type   string `json:"type"` // 必须是 "human_feedback_response"
	Accept bool   `json:"accept"`
	Notes  string `json:"notes,omitempty"`
}

// SourceDTO 是来源的对外结构。
type SourceDTO struct {
	N     int    `json:"n"`
	URL   string `json:"url"`
	Title string `json:"title"`
}

// ReportListItem 是历史列表中的一项（不含正文）。
type ReportListItem struct {
	ID        int64       `json:"id"`
	Query     string      `json:"query"`
	Title     string      `json:"title"`
	Sources   []SourceDTO `json:"sources"`
	CreatedAt string      `json:"created_at"` // 本地时间字符串
}

// ReportDetail 是单篇报告的完整内容。
type ReportDetail struct {
	ID        int64       `json:"id"`
	Query     string      `json:"query"`
	Title     string      `json:"title"`
	Content   string      `json:"content"`
	Sources   []SourceDTO `json:"sources"`
	CreatedAt string      `json:"created_at"`
}
