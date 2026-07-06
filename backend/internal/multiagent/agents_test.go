// Package multiagent — agents_test.go covers the pure
// helpers in agents.go that don't need a real LLM: section
// filtering, pseudo-section stripping, max-sections capping,
// and prompt-template integrity (the Writer prompt must
// contain the 5 required sections; the SecResearcher prompt
// must enforce the multi-agent word target). The full
// LLM-calling paths are tested via the integration tests
// in graph_test.go and draft_graph_test.go which use
// a fake LLM.
package multiagent

import (
	"strings"
	"testing"
)

func TestIsPseudoSection(t *testing.T) {
	cases := map[string]bool{
		"引言":           true,
		"  引言  ":       true,
		"INTRODUCTION": true,
		"Introduction": true,
		"intro":        true,
		"导言":           true,
		"概述":           true,
		"概要":           true,
		"结论":           true,
		"Conclusion":   true,
		"总结":           true,
		"小结":           true,
		"参考资料":         true,
		"参考文献":         true,
		"references":   true,
		"References":   true,
		"Bibliography": true,
		"Sources":      true,
		// Real section names should NOT be flagged.
		"研究背景":  false,
		"市场分析":  false,
		"技术架构":  false,
		"未来展望":  false,
		"风险与对策": false,
		"  ":    false,
	}
	for input, want := range cases {
		if got := isPseudoSection(input); got != want {
			t.Errorf("isPseudoSection(%q) = %v, want %v", input, got, want)
		}
	}
}

// TestWriterPrompt_HasAllRequiredSections guards the
// "multi-agent value" from being accidentally removed in
// future prompt edits. The Writer is ported from
// gpt-researcher writer.py and must return the 4-field
// JSON layout (table_of_contents / introduction /
// conclusion / sources). The "extra" prompts in
// AssembleReport + the JSON shape are what differentiate
// multi-agent from a flat concatenation.
func TestWriterPrompt_HasAllRequiredSections(t *testing.T) {
	// The Writer returns a JSON layout matching
	// gpt-researcher's sample_json.
	for _, kw := range []string{
		"table_of_contents",
		"introduction",
		"conclusion",
		"sources",
		"markdown",
		"apa",
	} {
		if !strings.Contains(WriterSystemPromptSampleSchema, kw) {
			t.Errorf("WriterSystemPromptSampleSchema missing %q (gpt-researcher sample_json shape)", kw)
		}
	}
	// AssembleReport must produce the 8 report sections
	// the multi-agent mode advertises.
	for _, section := range []string{
		"# ", "摘要", "目录", "## ", "结论", "参考资料",
	} {
		// Sample output structure is documented in the
		// AssembleReport function body. Spot-check by
		// building a tiny layout and asserting the
		// string contains each marker.
		layout := &ReportLayout{
			TableOfContents: "- Section A\n",
			Introduction:    "intro",
			Conclusion:      "conc",
			Sources:         []string{"[1] A — http://a"},
		}
		got := AssembleReport("Title", layout, []string{"Section A"}, []string{"draft"})
		if !strings.Contains(got, section) {
			t.Errorf("AssembleReport output missing %q\n  got: %q", section, got)
		}
	}
}

// TestSecResearcherPrompt_EnforcesWordTarget guards the
// per-section word target enforced by the SecResearcher
// user prompt (we pass totalWords to gpt-researcher's
// generate_report_prompt; the LLM responds with at least
// that many words).
func TestSecResearcherPrompt_EnforcesWordTarget(t *testing.T) {
	if !strings.Contains(SecResearcherUserPrompt("test section", "test materials", 1000), "at least 1000 words") {
		t.Error("SecResearcherUserPrompt must pass totalWords to enforce the per-section word target")
	}
	// gpt-researcher's required meta-instructions:
	// markdown, APA citations, in-text citation hyperlink.
	for _, kw := range []string{"markdown", "APA", "hyperlink"} {
		if !strings.Contains(SecResearcherUserPrompt("s", "m", 800), kw) {
			t.Errorf("SecResearcherUserPrompt missing gpt-researcher required keyword %q", kw)
		}
	}
}

// TestReviewerPrompt_HasActionableGuidelines guards the
// reviewer against vague "structure/accuracy" style
// feedback. Per gpt-researcher, the guidelines live in
// task config (not in the system prompt), so the assertion
// is on defaultReviewerGuidelines (set in graph.go).
// The ReviewerSystemPrompt itself must include the meta
// instruction about guidelines.
func TestReviewerPrompt_HasActionableGuidelines(t *testing.T) {
	requiredInSystem := []string{
		"expert research article reviewer",
		"specific guidelines",
	}
	for _, kw := range requiredInSystem {
		if !strings.Contains(ReviewerSystemPrompt, kw) {
			t.Errorf("ReviewerSystemPrompt missing required phrase %q", kw)
		}
	}
	// The actual per-section review criteria live in
	// defaultReviewerGuidelines (graph.go). Spot-check
	// a few to make sure they're concrete (not vague
	// "structure/accuracy" placeholders).
	requiredInGuidelines := []string{
		"[n] 引用",
		"避免同义重复",
		"同一术语",
	}
	joined := strings.Join(defaultReviewerGuidelines, "\n")
	for _, kw := range requiredInGuidelines {
		if !strings.Contains(joined, kw) {
			t.Errorf("defaultReviewerGuidelines missing required guideline %q", kw)
		}
	}
}
