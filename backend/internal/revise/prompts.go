// Package revise — prompts.go 定义报告修改的 prompt。
//
// 两类 prompt:
//  1. ClassifyInstructionPrompt: 判断用户指令属于哪种修改类型
//     (局部修改/翻译改风格/补充检索),决定走哪条处理路径。
//  2. ReviseSystemPrompt/UserPrompt: 实际修改报告的 prompt,
//     支持多轮对话历史 + 可选的新补充资料。
package revise

import (
	"fmt"
	"strings"
)

// ClassifySystemPrompt 让 LLM 把用户的修改指令分类成 3 种动作之一。
//
// 分类决定后续处理路径:
//   - supplement: 需要联网检索新资料(如"补充最新的 X 技术")
//   - local_edit: 局部修改,不需要新资料(如"结论改简洁点""扩第三章")
//   - restyle:    翻译/改风格,不需要新资料(如"翻译成英文""改活泼风格")
//
// 用 fast 档位(分类是小任务,要求快)。
const ClassifySystemPrompt = `你是一个修改指令分类器。给定用户的修改指令和当前报告的简要信息,判断这个指令属于以下哪一类:

- "supplement": 需要补充新信息/新资料才能完成。特征:提到"最新""补充""增加...方面""还缺""查一下"等暗示需要外部新知识。
- "local_edit": 针对现有内容的局部修改,不需要新资料。特征:提到"删减""扩充""重写""改简洁""详细点""调整顺序"等针对已有内容的操作。
- "restyle": 翻译或改变整体风格,不需要新资料。特征:提到"翻译""英文""中文""改活泼""改学术""改正式"等整体风格转换。

只输出一个 JSON 对象(不要 markdown 代码块,不要解释):
{"action": "supplement" | "local_edit" | "restyle", "search_query": "如果是 supplement,给出用于搜索的关键词(中文或英文,Google 风格);否则空字符串"}`

// ClassifyUserPrompt 构造分类器的 user 消息。
func ClassifyUserPrompt(instruction, reportExcerpt string) string {
	return fmt.Sprintf(`# 用户的修改指令
%s

# 当前报告的简要信息(前 500 字)
%s

请按 system 指令分类。`, instruction, truncateRunes(reportExcerpt, 500))
}

// ReviseSystemPrompt 是报告修改的 system 指令。
//
// 关键设计:
//   - 强调"基于原文修改",不是从头重写——保留原文的结构和引用
//   - 保留 [n] 引用编号(不重新编号,除非补充了新来源)
//   - 多轮对话时,理解上下文("刚才太啰嗦了,再精简"要基于上一轮修改)
//   - 补充检索场景:把新资料整合进相关章节,新来源编号顺延
const ReviseSystemPrompt = `你是一个研究报告修改助手。你的任务是根据用户的修改指令,对已有的研究报告进行修改,输出修改后的完整报告(Markdown 格式)。

核心原则:
1. **基于原文修改,不是从头重写**。保留原文的整体结构、章节划分、已有的 [n] 引用编号。
2. **保留 [n] 引用编号不变**(除非补充了新来源,新来源编号顺延)。
3. **只修改用户指令涉及的部分**,未提及的部分保持原样。不要擅自改动用户没要求的内容。
4. **多轮对话时理解上下文**:如果用户说"刚才太啰嗦了,再精简",这是针对上一轮修改的反馈,要在上一轮基础上继续改。
5. **补充资料时**:把新资料自然地整合进相关章节(不要简单堆在末尾),新来源用新的编号(顺延原文最大编号)。
6. 输出完整的修改后报告(从 # 标题开始),不要只输出修改的片段,不要加"以下是修改后的报告"之类的前言。`

// ReviseUserPrompt 构造修改的 user 消息。
//
// 参数:
//   - originalReport: 当前报告全文
//   - instruction: 用户本轮的修改指令
//   - newContext: 补充检索得到的资料(空串=无补充)
//   - history: 多轮对话历史(空=首次修改)
func ReviseUserPrompt(originalReport, instruction, newContext string, history []Message) string {
	var b strings.Builder
	b.WriteString("# 当前报告\n")
	b.WriteString(originalReport)
	b.WriteString("\n\n# 用户的修改指令\n")
	b.WriteString(instruction)

	// 补充资料(仅 supplement 场景有)。
	if strings.TrimSpace(newContext) != "" {
		b.WriteString("\n\n# 补充检索到的新资料(请整合进相关章节)\n")
		b.WriteString(newContext)
	}

	// 多轮对话历史(让 LLM 理解"刚才""再"等上下文指代)。
	if len(history) > 0 {
		b.WriteString("\n\n# 之前的修改对话历史(用于理解上下文)\n")
		for i, msg := range history {
			role := "用户"
			if msg.Role == "assistant" {
				role = "你(助手)"
			}
			// 历史里的 assistant 消息可能很长(是完整报告),截断避免 token 爆炸。
			content := msg.Content
			if msg.Role == "assistant" {
				content = truncateRunes(content, 1000)
			}
			fmt.Fprintf(&b, "%s: %s\n", role, content)
			_ = i
		}
	}

	b.WriteString("\n\n请输出完整的修改后报告(从 # 标题开始的完整 Markdown)。")
	return b.String()
}

// truncateRunes 把字符串截断到 maxRune 字符,超长加省略号。
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
