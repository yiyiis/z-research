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
		return "", nil, fmt.Errorf("planner json parse (raw=%q): %w",
			llm.Truncate(raw, 200), err)
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
		return "", nil, fmt.Errorf("planner returned no usable sections (raw=%q)",
			llm.Truncate(raw, 200))
	}
	return out.Title, cleaned, nil
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
		return nil, fmt.Errorf("writer json parse (raw=%q): %w",
			llm.Truncate(raw, 200), err)
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
