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
type ResearchRequest struct {
	Query string `json:"query"`
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
