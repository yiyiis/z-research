// Package prompts 提供研究流程中各阶段的中文 prompt 模板（纯函数，无副作用）。
package prompts

import (
	"fmt"
	"strings"
)

// ChooseAgentSystemPrompt 让 LLM 根据查询扮演最合适的领域专家，返回角色设定。
// 对齐 gpt-researcher 的 auto_agent_instructions 思路（中文版）。
func ChooseAgentSystemPrompt() string {
	return `你是一个擅长分析研究任务并分配专家角色的助手。
根据用户的研究查询，判断最适合回答该问题的专家角色，并直接输出一段简洁的"角色设定"。

输出格式（严格遵守，不要输出多余解释）：
角色：<一个简短的专业角色名，如"资深行业分析师" / "技术调研专家" / "财经研究员" 等>
指令：<一段第一人称的角色描述，说明你将如何完成任务，例如"作为一名资深行业分析师，你将基于可靠的资料，客观、严谨地撰写研究报告，注重数据与来源。">

只输出上述两行，不要输出 JSON、不要输出 markdown 代码块。`
}

// ChooseAgentUserPrompt 构造选角色的 user 消息。
func ChooseAgentUserPrompt(query string) string {
	return fmt.Sprintf("研究查询：%s", query)
}

// SubQuerySystemPrompt 指导 LLM 把一个大查询拆成若干个精准的搜索词。
func SubQuerySystemPrompt() string {
	return `你是一个搜索查询规划专家。
给定一个研究主题，请生成若干个 Google 风格的搜索词，用于在网上检索相关信息。
要求：
- 每个搜索词应聚焦一个具体的子主题或事实点；
- 搜索词应互相补充、尽量减少重叠；
- 使用与主题一致的语言（中文主题用中文搜索词，英文主题用英文搜索词）；
- 只输出一个 JSON 字符串数组，例如：["搜索词1", "搜索词2", "搜索词3"]；
- 不要输出任何解释性文字或 markdown 代码块。`
}

// SubQueryUserPrompt 构造生成子查询的 user 消息。
func SubQueryUserPrompt(query string, n int) string {
	return fmt.Sprintf("研究主题：%s\n\n请生成 %d 个搜索词，仅输出 JSON 字符串数组。", query, n)
}

// ReportSystemPrompt 生成撰写最终报告的 system 提示，带入角色。
func ReportSystemPrompt(role string, language string) string {
	langName := languageName(language)
	return fmt.Sprintf(`%s

你将基于提供的参考资料（已附带来源编号）撰写一份研究报告。要求：
1. 使用 %s 撰写；
2. 结构清晰，使用 Markdown 标题、列表等格式；
3. 内容必须严格基于参考资料，不要编造数据或事实；
4. 正文中引用具体信息时，在句末用 [n] 标注来源编号（n 对应参考资料列表中的编号）；
5. 报告末尾附"## 参考资料"列表，列出所有 [n] 对应的来源链接；
6. 客观、严谨，不加入个人观点。`, role, langName)
}

// ReportUserPrompt 构造撰写报告的 user 消息。
func ReportUserPrompt(query, context string, totalWords int) string {
	return fmt.Sprintf(`# 研究查询
%s

# 参考资料
%s

# 任务
请基于上述参考资料，围绕研究查询撰写一份约 %d 字的 Markdown 研究报告。
正文中引用资料时务必使用 [n] 标注来源，并在末尾给出"## 参考资料"列表。`,
		query, context, totalWords)
}

func languageName(lang string) string {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "zh", "chinese", "中文":
		return "中文"
	case "en", "english":
		return "English"
	default:
		return "中文"
	}
}

// ===== 详细报告（多轮拆分）的 prompt =====
// 对标 gpt-researcher 的 detailed_report：先大纲，再逐章独立检索+撰写。

// OutlineSystemPrompt 指导 LLM 生成报告大纲。
func OutlineSystemPrompt() string {
	return `你是一位资深研究报告架构师。请为给定研究主题设计一份结构清晰的报告大纲。
要求：
- 围绕主题，拆分出相互独立、逻辑互补的章节；
- 每个章节聚焦一个具体子主题，避免与"引言/结论/参考资料"这类通用章节重复；
- 使用与主题一致的语言（中文主题用中文）；
- 只输出一个 JSON 对象，格式：{"title":"报告总标题","sections":[{"title":"章节标题","desc":"该章一句话描述"}]}
- 不要输出任何解释性文字或 markdown 代码块。`
}

// OutlineUserPrompt 构造生成大纲的 user 消息。n 为目标章节数。
func OutlineUserPrompt(query, initialResearch string, n int) string {
	return fmt.Sprintf("# 研究主题\n%s\n\n# 已收集的初步资料\n%s\n\n# 任务\n请生成 %d 个章节的大纲，仅输出 JSON。",
		query, initialResearch, n)
}

// SectionSystemPrompt 构造撰写单章正文的 system 提示。
// existingHeaders 用于去重（已写过的章节标题），避免章节间内容重复。
func SectionSystemPrompt(role string, existingHeaders []string) string {
	dedup := ""
	if len(existingHeaders) > 0 {
		dedup = fmt.Sprintf("\n\n重要：以下章节已涵盖，请勿重复其内容，只聚焦本章主题：%s",
			strings.Join(existingHeaders, "、"))
	}
	return fmt.Sprintf(`%s

你将基于提供的该章专属参考资料撰写本章正文。要求：
1. 使用 Markdown，以二级标题（## 章节标题）开头；
2. 内容严格基于参考资料，不编造；
3. 引用具体信息时用 [n] 标注来源（n 对应资料中的编号）；
4. 聚焦本章主题，不写其他章节的内容；%s`, role, dedup)
}

// SectionUserPrompt 构造撰写单章的 user 消息。
func SectionUserPrompt(query, sectionTitle, sectionDesc, context string, wordsPerSection int) string {
	return fmt.Sprintf(`# 总研究主题
%s

# 本章标题
%s
# 本章要点
%s

# 本章专属参考资料
%s

# 任务
请基于上述资料撰写本章正文，约 %d 字。以 "## %s" 开头。`,
		query, sectionTitle, sectionDesc, context, wordsPerSection, sectionTitle)
}

// IntroSystemPrompt 撰写引言的 system 提示。
func IntroSystemPrompt(role string) string {
	return fmt.Sprintf(`%s

请为这份详细报告撰写一段引言（开头），要求：
1. 使用 Markdown，以一级标题（# 报告标题）开头，接 "## 引言"；
2. 简要介绍研究背景、报告要回答的核心问题、报告结构概览；
3. 不展开具体数据，保持提纲挈领。`, role)
}

// IntroUserPrompt 构造写引言的 user 消息。outline 是大纲文本。
func IntroUserPrompt(query, title, outline string) string {
	return fmt.Sprintf("# 研究主题\n%s\n\n# 报告标题\n%s\n\n# 报告大纲\n%s\n\n# 任务\n请撰写引言。", query, title, outline)
}

// ConclusionSystemPrompt 撰写结论的 system 提示。
func ConclusionSystemPrompt(role string) string {
	return fmt.Sprintf(`%s

请为这份详细报告撰写结论，要求：
1. 使用 Markdown，以 "## 结论" 开头；
2. 基于报告正文（已提供）总结核心发现与洞察；
3. 不引入正文未提及的新信息。`, role)
}

// ConclusionUserPrompt 构造写结论的 user 消息。fullBody 是已生成的全部正文（引言+各章）。
func ConclusionUserPrompt(query, fullBody string) string {
	// 若正文过长，截断以避免超出上下文。
	if len([]rune(fullBody)) > 12000 {
		fullBody = string([]rune(fullBody)[:12000]) + "\n...(已截断)"
	}
	return fmt.Sprintf("# 研究主题\n%s\n\n# 报告正文（引言+各章）\n%s\n\n# 任务\n请撰写结论。", query, fullBody)
}
