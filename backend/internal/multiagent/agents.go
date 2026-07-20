// Package multiagent — agents.go implements the per-role
// LLM-call functions used by the graph nodes.
//
// Each role is exposed in two ways:
//  1. A pure function (e.g. PlanOutline) that takes/returns
//     plain Go values and calls the LLM. These are unit
//     testable with a fake LLM.
//  2. A compose.InvokableLambda factory (e.g. NewPlannerNode)
//     that wraps the pure function for use as a graph node.
//     The factories are defined in graph.go / draft_graph.go
//     to keep this file free of Eino-specific code beyond
//     what the pure helpers need.
//
// The roles mirror gpt-researcher's multi_agents/agents/*:
//   - PlanOutline    ← editor.py::plan_research
//   - ReviewDraft    ← reviewer.py::review_draft
//   - ReviseDraft    ← reviser.py::revise_draft
//   - WriteSectionDraft ← gpt_researcher/prompts.py::generate_report_prompt
//     (subtopic_report mode)
//   - WriteReport    ← writer.py::write_sections
package multiagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"z-research/backend/internal/llm"
	"z-research/backend/internal/researcher"
)

// PlanOutline is the pure Planner implementation. Mirrors
// gpt-researcher EditorAgent.plan_research: it takes an
// initial research summary (produced by the Browser node) and
// a max-sections cap, and returns the parsed outline.
//
// initialResearch is the markdown summary of the Browser
// node's full single-agent run on the same query. This is
// the key difference vs. the previous version: the planner
// now sees the actual research landscape before deciding
// the section structure, which produces much better
// outlines (gpt-researcher's main quality lever).
//
// humanFeedback is non-empty only when the human reviewed
// the previous plan and asked for revisions; in that case
// the planner uses it to refocus the outline.
func PlanOutline(ctx context.Context, l *llm.LLM, query, initialResearch, humanFeedback string, maxSections int) (string, []string, error) {
	if maxSections <= 0 {
		maxSections = 4
	}
	// 用 strategic 档位（Planner 规划章节结构=深度规划/拆子主题）。
	raw, err := l.StrategicChat(ctx,
		PlannerSystemPrompt,
		PlannerUserPrompt(query, initialResearch, humanFeedback, maxSections),
	)
	if err != nil {
		return "", nil, fmt.Errorf("planner llm: %w", err)
	}
	jsonStr := llm.ExtractJSON(raw)

	var out struct {
		Title    string   `json:"title"`
		Date     string   `json:"date"`
		Sections []string `json:"sections"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &out); err != nil {
		// 降级：Planner JSON 解析失败（思考模型截断等高发场景）。
		// 不让整个多智能体流程崩——用 query 拆出兜底大纲（2 个通用章节）。
		// 业务上能继续走，质量略降，优于全盘失败。
		title, sections := fallbackOutline(query, maxSections)
		return title, sections, nil
	}
	if out.Title == "" {
		out.Title = "研究综述"
	}
	// Strip pseudo-sections and cap.
	cleaned := make([]string, 0, len(out.Sections))
	for _, s := range out.Sections {
		s = strings.TrimSpace(s)
		if s == "" || isPseudoSection(s) {
			continue
		}
		cleaned = append(cleaned, s)
		if len(cleaned) >= maxSections {
			break
		}
	}
	if len(cleaned) == 0 {
		// 降级：模型返回的 sections 全是伪章节或为空。
		title, sections := fallbackOutline(query, maxSections)
		return title, sections, nil
	}
	return out.Title, cleaned, nil
}

// fallbackOutline 在 Planner LLM 失败时生成兜底大纲。
// 用 query 作为标题，按 maxSections 给出通用章节名。
// 这保证多智能体流程能继续走（researcher/writer 仍会工作），只是章节不够个性化。
func fallbackOutline(query string, maxSections int) (string, []string) {
	if maxSections <= 0 {
		maxSections = 4
	}
	if maxSections > 4 {
		maxSections = 4
	}
	title := query
	if len([]rune(title)) > 40 {
		title = string([]rune(title)[:40]) + "…"
	}
	// 通用章节模板（按主题维度拆分，适用性较广）。
	templates := []string{
		"背景与定义",
		"核心原理与架构",
		"应用场景与实践",
		"挑战与未来趋势",
	}
	sections := templates[:min(len(templates), maxSections)]
	return "研究综述：" + title, sections
}

// min 返回较小值（Go 1.21+ 内置了 min，这里兼容性定义避免歧义）。
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// isPseudoSection returns true for the generic section
// names that the Writer node generates itself ("引言",
// "结论", "参考资料", and their English equivalents).
func isPseudoSection(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "引言", "introduction", "intro", "导言", "概述", "概要":
		return true
	case "结论", "conclusion", "总结", "小结":
		return true
	case "参考资料", "参考文献", "references", "bibliography", "sources":
		return true
	}
	return false
}

// BrowserResearch runs a full single-agent research pass to
// produce the initial_research summary that the Planner
// consumes. This mirrors gpt-researcher multi_agents'
// "browser" node, which delegates to the single-agent
// GPTResearcher.run_initial_research.
//
// Implementation: reuses researcher.Researcher.Conduct (the
// existing single-agent "collect material" pipeline) but
// returns a markdown summary of the gathered sources
// (titles + URLs) suitable for the Planner's prompt. The
// per-section drafts that the original gpt-researcher
// produces are NOT done here — the Browser pass is just
// the "what sources exist" scan.
func BrowserResearch(ctx context.Context, inner *researcher.Researcher, query string) (string, error) {
	res, err := inner.Conduct(ctx, query, nil)
	if err != nil {
		return "", fmt.Errorf("browser research: %w", err)
	}
	// Compose a markdown summary: list of source titles
	// with brief context snippets. The Planner reads
	// this to decide section structure.
	var b strings.Builder
	b.WriteString("已检索到的资料（用于规划文章大纲）：\n\n")
	for _, s := range res.Sources {
		fmt.Fprintf(&b, "- [%d] %s — %s\n", s.N, s.Title, s.URL)
	}
	if res.Context != "" {
		// Include a brief excerpt of the consolidated
		// context (first 2000 chars) so the Planner
		// sees what topics are covered.
		excerpt := res.Context
		if len([]rune(excerpt)) > 2000 {
			excerpt = string([]rune(excerpt)[:2000]) + "…(已截断)"
		}
		b.WriteString("\n资料摘要（前 2000 字）：\n")
		b.WriteString(excerpt)
	}
	return b.String(), nil
}

// ReviewDraft is the pure Reviewer implementation. Returns:
//   - accept=true, notes=""  → draft is good
//   - accept=false, notes=…  → revise with these notes
//
// revisionNotes is non-empty on subsequent review rounds;
// the prompt is "soft" in that case (only ask for critical
// fixes, since the reviser has already worked on previous
// notes). Mirrors gpt-researcher reviewer.py::review_draft.
func ReviewDraft(ctx context.Context, l *llm.LLM, section, draft, guidelines, revisionNotes string) (accept bool, notes string, err error) {
	// 用 fast 档位（Reviewer 产出 accept/revise 判定，结构化短输出）。
	raw, err := l.FastChat(ctx,
		ReviewerSystemPrompt,
		ReviewerUserPrompt(guidelines, draft, revisionNotes),
	)
	if err != nil {
		return false, "", fmt.Errorf("reviewer llm: %w", err)
	}

	// gpt-researcher treats the literal substring "None"
	// in the response as "accept". Anything else is
	// treated as revise notes (free-form string).
	//
	// Some models wrap in ``` or add leading prose; we
	// trim and check. We also strip a JSON wrapper if
	// the model tried to be helpful and return a
	// {"decision": "..."} object instead of plain text.
	rawTrim := strings.TrimSpace(raw)
	// Try to extract from a JSON wrapper first.
	jsonStr := llm.ExtractJSON(rawTrim)
	if jsonStr != rawTrim {
		var obj struct {
			Decision string `json:"decision"`
			Notes    string `json:"notes"`
		}
		if err := json.Unmarshal([]byte(jsonStr), &obj); err == nil {
			switch strings.ToLower(strings.TrimSpace(obj.Decision)) {
			case "accept", "":
				if strings.TrimSpace(obj.Notes) == "" {
					return true, "", nil
				}
			}
			return false, strings.TrimSpace(obj.Notes), nil
		}
	}

	// Plain-text check: "None" anywhere in the response
	// means accept (per gpt-researcher).
	if strings.Contains(rawTrim, "None") {
		return true, "", nil
	}
	return false, rawTrim, nil
}

// ReviseDraft is the pure Reviser implementation. Returns
// the rewritten draft and a one-line summary of the changes
// (consumed by the next Reviewer pass).
//
// gpt-researcher's expected JSON shape is:
//
//	{"draft": {<section title>: "<revised draft>"},
//	 "revision_notes": "..."}
//
// We unwrap that nested structure to a flat string.
func ReviseDraft(ctx context.Context, l *llm.LLM, section, draft, reviewNotes string) (newDraft, revisionNotes string, err error) {
	raw, err := l.Chat(ctx,
		ReviserSystemPrompt,
		ReviserUserPrompt(section, draft, reviewNotes),
	)
	if err != nil {
		return "", "", fmt.Errorf("reviser llm: %w", err)
	}
	jsonStr := llm.ExtractJSON(raw)

	// The "draft" field is a map keyed by the section
	// title. Try the gpt-researcher shape first; fall
	// back to a flat "draft" string.
	var nested struct {
		Draft         map[string]string `json:"draft"`
		RevisionNotes string            `json:"revision_notes"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &nested); err == nil && len(nested.Draft) > 0 {
		// Pick the value for our section; if the model
		// used a different key, take the first.
		if v, ok := nested.Draft[section]; ok {
			return v, strings.TrimSpace(nested.RevisionNotes), nil
		}
		for _, v := range nested.Draft {
			return v, strings.TrimSpace(nested.RevisionNotes), nil
		}
	}

	// Fallback: flat draft string.
	var flat struct {
		Draft         string `json:"draft"`
		RevisionNotes string `json:"revision_notes"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &flat); err == nil {
		if strings.TrimSpace(flat.Draft) != "" {
			return flat.Draft, strings.TrimSpace(flat.RevisionNotes), nil
		}
	}

	// 最终降级：模型输出无法解析（常见于思考模型 <think> 截断、
	// max_tokens 用完只输出了推理过程没产出 JSON）。
	// 不让整个多智能体流程崩掉——返回原 draft 不变，
	// 这样 reviewer 会看到"草稿未修改"，避免死循环。
	if strings.TrimSpace(draft) != "" {
		return draft, "（修订失败：模型未产出有效 JSON，已保留原草稿）", nil
	}
	return "", "", fmt.Errorf("reviser json parse (raw=%q)", llm.Truncate(raw, 200))
}

// WriteSectionDraft is the pure SecResearcher (per-section
// initial draft writer) implementation. Takes the section
// title and the numbered material block produced by
// researcher.Researcher.Conduct, returns the draft text.
//
// This mirrors gpt-researcher's subtopic_report mode where
// the section title is the "query" and the gathered context
// is the research_data, calling generate_report_prompt.
func WriteSectionDraft(ctx context.Context, l *llm.LLM, section, materials string, totalWords int) (string, error) {
	if totalWords <= 0 {
		totalWords = 1000
	}
	raw, err := l.Chat(ctx,
		SecResearcherSystemPrompt,
		SecResearcherUserPrompt(section, materials, totalWords),
	)
	if err != nil {
		return "", fmt.Errorf("sec-researcher llm: %w", err)
	}
	return strings.TrimSpace(raw), nil
}

// WriteReportLayout is the layout-only Writer: generates the
// table_of_contents, introduction, conclusion, and sources
// list. Mirrors gpt-researcher writer.py::write_sections.
//
// Returns a structured Layout that the engine can use to
// assemble the final report around the per-section drafts.
type ReportLayout struct {
	TableOfContents string
	Introduction    string
	Conclusion      string
	Sources         []string
}

// WriteReportLayout calls the LLM with the Writer prompt and
// research data JSON (a serialized list of section drafts),
// returning the parsed layout.
func WriteReportLayout(ctx context.Context, l *llm.LLM, query, title, researchDataJSON string) (*ReportLayout, error) {
	raw, err := l.Chat(ctx,
		WriterSystemPrompt,
		WriterUserPrompt(query, title, researchDataJSON),
	)
	if err != nil {
		return nil, fmt.Errorf("writer llm: %w", err)
	}
	jsonStr := llm.ExtractJSON(raw)

	var out struct {
		TableOfContents string   `json:"table_of_contents"`
		Introduction    string   `json:"introduction"`
		Conclusion      string   `json:"conclusion"`
		Sources         []string `json:"sources"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &out); err != nil {
		// 降级：Writer JSON 解析失败时，返回最小 layout（空 intro/conclusion/sources）。
		// AssembleReport 会用 sections + drafts 拼出仍可读的报告（缺摘要和结论，
		// 质量降级但不至于崩溃）。
		return &ReportLayout{}, nil
	}
	return &ReportLayout{
		TableOfContents: out.TableOfContents,
		Introduction:    out.Introduction,
		Conclusion:      out.Conclusion,
		Sources:         out.Sources,
	}, nil
}

// AssembleReport builds the final markdown report from the
// layout + per-section drafts. Mirrors how gpt-researcher
// combines writer.py's output with research_data into the
// final report markdown.
//
// Layout (introduction, sections, conclusion, sources) is
// placed in a standard report shape:
//
//	# Title
//	## 摘要 (or intro)
//
//	## 目录
//	- Section 1
//	- Section 2
//
//	## Section 1
//	<draft 1>
//
//	## Section 2
//	<draft 2>
//
//	## 结论
//	<conclusion>
//
//	## 参考资料
//	- [1] ...
func AssembleReport(title string, layout *ReportLayout, sections []string, drafts []string) string {
	var b strings.Builder
	// Title.
	if title != "" {
		b.WriteString("# ")
		b.WriteString(title)
		b.WriteString("\n\n")
	}
	// Introduction.
	if layout != nil && layout.Introduction != "" {
		b.WriteString("## 摘要\n\n")
		b.WriteString(layout.Introduction)
		b.WriteString("\n\n")
	}
	// Table of contents (use the LLM-generated one if
	// available, otherwise generate a simple list).
	b.WriteString("## 目录\n\n")
	if layout != nil && layout.TableOfContents != "" {
		b.WriteString(layout.TableOfContents)
		b.WriteString("\n\n")
	} else {
		for i, s := range sections {
			fmt.Fprintf(&b, "%d. %s\n", i+1, s)
		}
		b.WriteString("\n")
	}
	// Body sections.
	for i, s := range sections {
		if i < len(drafts) {
			b.WriteString("## ")
			b.WriteString(s)
			b.WriteString("\n\n")
			b.WriteString(drafts[i])
			b.WriteString("\n\n")
		}
	}
	// Conclusion.
	if layout != nil && layout.Conclusion != "" {
		b.WriteString("## 结论\n\n")
		b.WriteString(layout.Conclusion)
		b.WriteString("\n\n")
	}
	// Sources.
	if layout != nil && len(layout.Sources) > 0 {
		b.WriteString("## 参考资料\n\n")
		for _, s := range layout.Sources {
			b.WriteString("- ")
			b.WriteString(s)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// ---- 第三个循环：事实核查（fact_checker） ----

// FactCheckResult 是 fact_checker 节点的产出。
type FactCheckResult struct {
	// Verdict: "pass" 表示核查通过；"fail" 表示存在需要重写的问题。
	// 路由信号据此决定：pass → factPassSentinel → visualizer；fail → 核查报告 → writer。
	Verdict string `json:"verdict"`
	// Report 是给 writer 的修订建议（中文），verdict=pass 时为空。
	Report string `json:"report"`
}

// FactCheck 让 LLM 对报告正文做事实性校验。
//
// 对齐 session 设计的关键约束：
//   - 只看报告正文（intro + 各 section data + conclusion），
//     不看 URL、不看引用编号。这意味着核查基于"论断本身是否自洽/
//     是否与常识冲突"，而非"引用是否真实存在"。
//   - 用 fast 档位（结构化短输出，类似 reviewer）。
//
// 返回的 FactCheckResult.Verdict 决定 graph 路由。
func FactCheck(ctx context.Context, l *llm.LLM, report string) (*FactCheckResult, error) {
	raw, err := l.FastChat(ctx, FactCheckerSystemPrompt, FactCheckerUserPrompt(report))
	if err != nil {
		// 降级：fact_checker LLM 调用失败时放行（verdict=pass），
		// 不让核查循环卡死整个流程。核查是"锦上添花"，失败时信任 writer。
		return &FactCheckResult{Verdict: "pass", Report: ""}, nil
	}
	jsonStr := llm.ExtractJSON(raw)
	var out struct {
		Verdict string `json:"verdict"`
		Report  string `json:"report"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &out); err != nil {
		// 降级：JSON 解析失败同样放行。
		return &FactCheckResult{Verdict: "pass", Report: ""}, nil
	}
	// 规范化 verdict：任何非明确 pass 的都视为 fail（保守策略）。
	if strings.EqualFold(strings.TrimSpace(out.Verdict), "pass") {
		out.Verdict = "pass"
	} else {
		out.Verdict = "fail"
	}
	return &FactCheckResult{Verdict: out.Verdict, Report: out.Report}, nil
}

// ---- 可视化（visualizer） ----

// Visualize 让 LLM 从完整报告抽取元数据 + 可选 mermaid 概览。
//
// 用 smart 档位（需要从全文理解结构）。产出的 Visuals 字符串会被
// publisher 附加到最终报告末尾。
func Visualize(ctx context.Context, l *llm.LLM, title, report string) (string, error) {
	raw, err := l.Chat(ctx, VisualizerSystemPrompt, VisualizerUserPrompt(title, report))
	if err != nil {
		return "", fmt.Errorf("visualizer llm: %w", err)
	}
	return strings.TrimSpace(raw), nil
}

// ---- 发布（publisher） ----

// Publish 是 publisher 节点的核心：把报告正文 + 核查摘要 + 可视化元数据
// 组装成最终输出。纯函数，不调 LLM。
//
// 这是解决 engine.go:180 "Sources 未回传 FinalReport" TODO 的接入点：
// publisher 把 Sources 也编码进最终输出（通过 state 传递，由 Engine.Run
// 从 state 取出）。当前实现把核查摘要与可视化作为附录附加。
func Publish(report, factCheckReport, visuals string, sources []researcher.Source) string {
	var b strings.Builder
	b.WriteString(report)
	appended := false
	// 事实核查摘要附录（仅当核查未完全通过时才有内容）。
	if strings.TrimSpace(factCheckReport) != "" {
		b.WriteString("\n\n## 事实核查纪要\n\n")
		b.WriteString(factCheckReport)
		appended = true
	}
	// 可视化元数据附录。
	if strings.TrimSpace(visuals) != "" {
		if !appended {
			b.WriteString("\n")
		}
		b.WriteString("\n## 报告元数据\n\n")
		b.WriteString(visuals)
		appended = true
	}
	// 来源列表（若原报告未包含）。
	if !strings.Contains(report, "参考资料") && len(sources) > 0 {
		if !appended {
			b.WriteString("\n")
		}
		b.WriteString("\n## 参考资料\n\n")
		for _, s := range sources {
			fmt.Fprintf(&b, "%d. %s — %s\n", s.N, s.Title, s.URL)
		}
	}
	return b.String()
}
