package main

import (
	"fmt"
	"strings"
)

// chooseAgentSystemPrompt 让 LLM 根据查询扮演最合适的领域专家，返回角色设定。
// 对齐 gpt-researcher 的 auto_agent_instructions 思路（中文版）。
func chooseAgentSystemPrompt() string {
	return `你是一个擅长分析研究任务并分配专家角色的助手。
根据用户的研究查询，判断最适合回答该问题的专家角色，并直接输出一段简洁的"角色设定"。

输出格式（严格遵守，不要输出多余解释）：
角色：<一个简短的专业角色名，如"资深行业分析师" / "技术调研专家" / "财经研究员" 等>
指令：<一段第一人称的角色描述，说明你将如何完成任务，例如"作为一名资深行业分析师，你将基于可靠的资料，客观、严谨地撰写研究报告，注重数据与来源。">

只输出上述两行，不要输出 JSON、不要输出 markdown 代码块。`
}

func chooseAgentUserPrompt(query string) string {
	return fmt.Sprintf("研究查询：%s", query)
}

// subQuerySystemPrompt 指导 LLM 把一个大查询拆成若干个精准的搜索词。
func subQuerySystemPrompt() string {
	return `你是一个搜索查询规划专家。
给定一个研究主题，请生成若干个 Google 风格的搜索词，用于在网上检索相关信息。
要求：
- 每个搜索词应聚焦一个具体的子主题或事实点；
- 搜索词应互相补充、尽量减少重叠；
- 使用与主题一致的语言（中文主题用中文搜索词，英文主题用英文搜索词）；
- 只输出一个 JSON 字符串数组，例如：["搜索词1", "搜索词2", "搜索词3"]；
- 不要输出任何解释性文字或 markdown 代码块。`
}

func subQueryUserPrompt(query string, n int) string {
	return fmt.Sprintf("研究主题：%s\n\n请生成 %d 个搜索词，仅输出 JSON 字符串数组。", query, n)
}

// reportSystemPrompt 生成撰写最终报告的 system 提示，带入角色。
func reportSystemPrompt(role string, language string) string {
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

func reportUserPrompt(query, context string, totalWords int) string {
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
