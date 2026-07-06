// Package researcher 实现研究编排引擎——gpt-researcher 默认固定工作流的 Go 移植：
//
//	plan(子查询) → 并发{search → fetch → compress} → 合并上下文
//
// 它本身不做 ReAct 循环；控制流是确定性的，深度/数量由 Config 严格限定。
// 引擎通过 Progress 回调上报各阶段进度，供 CLI 与 HTTP/SSE 复用（见 engine.go）。
package researcher

// Source 记录一条被引用的来源（带全局编号，用于报告中的 [n] 引用）。
type Source struct {
	N     int    `json:"n"`     // 引用编号，从 1 开始
	URL   string `json:"url"`   // 来源 URL
	Title string `json:"title"` // 来源标题
}

// Result 是一次研究的产出。
type Result struct {
	Context string   `json:"-"`       // 带来源编号的参考资料文本（供报告 prompt 用，不直接返回前端）
	Sources []Source `json:"sources"` // 来源列表
}

// Stage 标识研究流程的阶段，用于 Progress 事件。
type Stage string

const (
	StageRole        Stage = "role"        // 选角色
	StagePlanning    Stage = "planning"    // 规划子查询
	StageSearching   Stage = "searching"   // 搜索
	StageFetching    Stage = "fetching"    // 抓取网页
	StageCompressing Stage = "compressing" // 压缩
	StageWriting     Stage = "writing"     // 撰写报告（单 Agent / 简报）
	StageOutline     Stage = "outline"     // 生成大纲（详细报告）
	StageSection     Stage = "section"     // 撰写某章节（详细报告）
)

// ReportType 标识报告类型。
type ReportType string

const (
	// ReportBrief 是默认的简报模式：单次 LLM 调用，篇幅受 max_tokens 约束（约 3000-4000 中文字）。
	ReportBrief ReportType = "brief"
	// ReportDetailed 是详细报告：先生成大纲，每个章节各自独立检索 + 流式写一段 + 拼接，
	// 突破单次调用篇幅上限，可生成万字长报告。对标 gpt-researcher 的 detailed_report。
	ReportDetailed ReportType = "detailed"
)

// OutlineSection 是详细报告大纲中的一个章节。
type OutlineSection struct {
	Title string `json:"title"` // 章节标题
	Desc  string `json:"desc"`  // 章节一句话描述（指导该章检索与撰写）
}

// Progress 是一次进度上报事件。
type Progress struct {
	Stage        Stage  `json:"stage"`                   // 阶段
	Message      string `json:"message,omitempty"`       // 人类可读说明
	SubQuery     string `json:"subquery,omitempty"`      // 当前子查询（搜索/抓取阶段）
	Found        int    `json:"found,omitempty"`         // 找到的结果数（搜索阶段）
	URL          string `json:"url,omitempty"`           // 当前 URL（抓取阶段）
	SectionTitle string `json:"section_title,omitempty"` // 当前章节（详细报告的 section 阶段）
}

// EventFn 是进度回调。实现方（CLI 打印 / HTTP SSE 推送）各决定如何消费。
// 返回 error 可用于提前中止（例如客户端断开时取消上下文）。
type EventFn func(p Progress)
