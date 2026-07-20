// Package revise 实现报告的对话式修改引擎。
//
// 支持四类修改能力(用户通过自然语言指令触发,引擎自动分类):
//   - 局部修改 local_edit: "结论改简洁""扩第三章"(不检索)
//   - 翻译改风格 restyle: "翻译成英文""改学术风格"(不检索)
//   - 补充检索 supplement: "补充最新的 MoE 技术"(先检索再改)
//   - 多轮迭代: 保留对话历史,理解"刚才太啰嗦,再精简"等上下文
//
// 与现有研究引擎(researcher.EngineIface)的区别:
//   - 研究引擎从零生成报告(走 5 节点 graph)
//   - revise 基于已有报告修改(不走 graph,直接调 LLM)
//   - revise 通过独立 WS 端点 /ws/revise 接入,不动现有 /ws
package revise

// Action 是用户修改指令的分类,决定处理路径。
type Action int

const (
	ActionUnknown Action = iota
	// ActionSupplement 需要补充检索:用户要新增资料(如"补充最新的 X")。
	// 处理:先用 fast 档从指令抽 search_query → 调 researcher.Conduct → 整合进报告。
	ActionSupplement
	// ActionLocalEdit 局部修改:针对已有内容调整(如"结论改简洁""扩第三章")。
	// 处理:直接调 LLM 修改,不检索。
	ActionLocalEdit
	// ActionRestyle 翻译/改风格:整体风格转换(如"翻译英文""改活泼")。
	// 处理:直接调 LLM 修改,不检索。
	ActionRestyle
)

// String 返回 Action 的可读名称(用于日志/进度上报)。
func (a Action) String() string {
	switch a {
	case ActionSupplement:
		return "supplement"
	case ActionLocalEdit:
		return "local_edit"
	case ActionRestyle:
		return "restyle"
	default:
		return "unknown"
	}
}

// Message 是多轮对话的一条消息(对齐 ChatGPT 的 role/content 模型)。
type Message struct {
	Role    string `json:"role"`    // "user" 或 "assistant"
	Content string `json:"content"` // 消息内容(assistant 的是完整报告)
}

// Request 是一次修改请求的入参。
type Request struct {
	ReportID    int64     `json:"report_id"`    // 待修改的报告 ID
	Instruction string    `json:"instruction"`  // 用户的修改指令
	History     []Message `json:"history"`      // 之前的对话历史(首次修改为空)
}

// Result 是一次修改的产出。
type Result struct {
	NewReport      string             `json:"new_report"`       // 修改后的完整报告
	NewSources     []SourceRow        `json:"new_sources,omitempty"` // 补充检索新增的来源(仅 supplement)
	SavedReportID  int64              `json:"saved_report_id"`  // 持久化后的新报告 ID(另存,不覆盖原报告)
	Action         Action             `json:"action"`           // 实际执行的修改类型(用于前端展示)
}

// SourceRow 是来源信息(与 collection.Source / researcher.Source 对齐,但 revise 包不依赖它们)。
type SourceRow struct {
	N     int    `json:"n"`
	URL   string `json:"url"`
	Title string `json:"title"`
}
