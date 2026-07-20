// Package eval — prompts.go 定义 LLM-as-Judge 的评分 prompt。
//
// 对齐 RAGAS(Retrieval Augmented Generation Assessment)的思路:
// 用一个 LLM 当评委,按多维度 rubric 给报告打分,并给出扣分原因。
// 评分维度结合 RAGAS 的 faithfulness/answer relevancy 与工程经验:
//   - coverage    完整性:是否覆盖了查询的核心方面
//   - citation    引用质量:[n] 引用是否充分、是否对应真实来源
//   - structure   结构组织:章节是否合理、逻辑是否递进
//   - objectivity 客观性:是否避免主观臆断、是否基于资料
//   - readability 可读性:语言是否流畅、术语是否一致
package eval

import (
	"fmt"
	"strings"
)

// JudgeSystemPrompt 是评委的 system 指令,明确评分维度和 rubric。
//
// 关键设计:
//   - 每个维度 0-10 分,有明确的及格线(6)和优秀线(8)定义
//   - 要求给出扣分原因(不只是数字),便于人理解
//   - 强调基于"报告 + 来源列表"评分,不引入评委自己的知识(避免偏见)
//   - 输出严格 JSON(配合 llm.ExtractJSON + chatJSONWith 的修复重试)
const JudgeSystemPrompt = `你是一位严格、专业的研究报告评审专家。你的任务是对一份研究报告按多维度打分(每维 0-10 分),并给出扣分原因。

评分维度与 rubric(严格按此标准):

1. coverage(完整性):报告是否充分覆盖了研究查询的核心方面?
   - 9-10:全面覆盖,无明显遗漏,包含关键背景/现状/趋势
   - 6-8:覆盖主要方面,但有少量次要内容缺失
   - 3-5:有明显重要方面未涉及
   - 0-2:严重偏题或内容空洞

2. citation(引用质量):正文中的 [n] 引用是否充分?是否对应真实来源?
   - 9-10:关键论断几乎都有引用,引用编号与来源列表一一对应
   - 6-8:多数论断有引用,偶有遗漏
   - 3-5:引用稀疏,很多论断无支撑
   - 0-2:几乎无引用,或引用编号与来源对不上

3. structure(结构组织):章节划分是否合理?逻辑是否递进?
   - 9-10:章节清晰,逻辑层层递进,有引言/正文/结论的完整结构
   - 6-8:结构基本合理,但有少量章节界限模糊
   - 3-5:结构松散,章节之间缺乏逻辑联系
   - 0-2:无结构,内容堆砌

4. objectivity(客观性):是否避免主观臆断?是否基于资料而非臆测?
   - 9-10:全程客观,所有论断基于资料,无"我认为""显然"等主观表达
   - 6-8:基本客观,偶有轻度主观措辞
   - 3-5:有较多主观判断或未经支撑的断言
   - 0-2:充满主观臆测,与资料脱节

5. readability(可读性):语言是否流畅?术语是否一致?是否有错别字/病句?
   - 9-10:语言流畅专业,术语统一,无错别字
   - 6-8:基本可读,有少量表述不清或术语不一致
   - 3-5:语言生硬,有不少病句
   - 0-2:难以阅读

重要原则:
- 只基于给定的"研究报告 + 来源列表"评分,不要引入你自己的知识判断事实对错(那是 fact_checker 的工作)。
- 严格按 rubric 给分,不要一律打高分或一律打低分。
- 扣分原因要具体(指出哪一段/哪个论断有问题),不要泛泛而谈。

你必须只输出一个 JSON 对象,格式如下(不要输出任何 markdown 代码块或解释文字):
{
  "dimensions": {
    "coverage": {"score": 0-10的数字, "note": "扣分原因(中文)"},
    "citation": {"score": 数字, "note": "原因"},
    "structure": {"score": 数字, "note": "原因"},
    "objectivity": {"score": 数字, "note": "原因"},
    "readability": {"score": 数字, "note": "原因"}
  },
  "summary": "一句话总评(中文,50字以内)"
}`

// JudgeUserPrompt 构造评委的 user 消息,带上报告原文和来源列表。
//
// 来源列表单独列出,便于评委核对 [n] 引用是否真实存在。
// 报告过长时截断(评委也看不完超长报告,且省 token)。
func JudgeUserPrompt(query, report string, sources []SourceRow) string {
	// 来源列表格式化。
	var srcBuf strings.Builder
	srcBuf.WriteString("(无来源)")
	if len(sources) > 0 {
		srcBuf.Reset()
		for _, s := range sources {
			fmt.Fprintf(&srcBuf, "[%d] %s — %s\n", s.N, s.Title, s.URL)
		}
	}

	// 报告过长截断(保留头部 + 尾部,中间省略,因为头尾通常含标题/结论最关键)。
	reportDisplay := report
	const maxRunes = 8000
	if len([]rune(report)) > maxRunes {
		r := []rune(report)
		half := maxRunes / 2
		reportDisplay = string(r[:half]) + "\n\n...(中间部分省略,仅供评审参考)...\n\n" + string(r[len(r)-half:])
	}

	return fmt.Sprintf(`# 研究查询
%s

# 待评审的报告
%s

# 报告声明的来源列表(供核对 [n] 引用)
%s

请按 system 指令的 rubric 给出评分 JSON。`, query, reportDisplay, srcBuf.String())
}
