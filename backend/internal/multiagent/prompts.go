// Package multiagent — prompts.go defines the system + user
// prompts for each multi-agent role. The role definitions
// are ported from gpt-researcher's multi_agents/agents/ (which
// itself follows the STORM paper, arXiv:2402.14207), with
// minimal Chinese adaptation.
//
// The prompts follow the SAME structure and decision rules
// as the original Python implementation. Each role produces
// the SAME JSON shape that gpt-researcher expects, so a
// future side-by-side comparison (e.g. running both on the
// same query) can be apples-to-apples.
//
// Mapping to gpt-researcher source files:
//   - PlannerSystemPrompt  ← multi_agents/agents/editor.py::_create_planning_prompt
//   - ReviewerSystemPrompt ← multi_agents/agents/reviewer.py::TEMPLATE + review_prompt
//   - ReviserSystemPrompt  ← multi_agents/agents/reviser.py::sample_revision_notes
//   - WriterSystemPrompt   ← multi_agents/agents/writer.py::sample_json
//   - SecResearcherSystemPrompt ← gpt_researcher/prompts.py::generate_report_prompt (subtopic_report mode)
package multiagent

import "fmt"

// =============================================================================
// Planner (Editor)
// =============================================================================
//
// Original: "You are a research editor. Your goal is to oversee
// the research project from inception to completion. Your main
// task is to plan the article section layout based on an
// initial research summary."
//
// KEY DIFFERENCE vs. my previous version: the Planner does NOT
// just receive the user's query — it receives an initial
// research summary produced by the Browser node (which itself
// calls the full single-agent GPTResearcher pipeline). This
// is what makes gpt-researcher's outline quality much higher:
// the planner can see what sources exist before deciding the
// sections.

// PlannerSystemPrompt is the system prompt for the Planner
// role. Mirrors gpt-researcher's EditorAgent.
const PlannerSystemPrompt = `You are a research editor. Your goal is to oversee the research project from inception to completion. Your main task is to plan the article section layout based on an initial research summary.
`

// PlannerUserPrompt builds the user prompt for the Planner.
// It closely mirrors gpt-researcher's _format_planning_instructions:
// date, initial research summary, optional human feedback,
// max sections, and the required JSON output schema.
func PlannerUserPrompt(query, initialResearch, humanFeedback string, maxSections int) string {
	feedback := ""
	if humanFeedback != "" {
		feedback = fmt.Sprintf(
			"Human feedback: %s. You must plan the sections based on the human feedback.",
			humanFeedback,
		)
	}
	if maxSections <= 0 {
		maxSections = 4
	}
	return fmt.Sprintf(`Today's date is %s
Research summary report: "%s"
%s

Your task is to generate an outline of sections headers for the research project based on the research summary report above.
You must generate a maximum of %d section headers.
You must focus ONLY on related research topics for subheaders and do NOT include introduction, conclusion and references.
You must return nothing but a JSON with the fields 'title' (str) and 'sections' (maximum %d section headers) with the following structure:
'{title: string research title, date: today's date, sections: ['section header 1', 'section header 2', 'section header 3' ...']}'.

The original query from the user was: "%s"`, /* keep query in scope for context */
		"", // date is filled in by caller; left as placeholder
		initialResearch,
		feedback,
		maxSections, maxSections,
		query,
	)
}

// =============================================================================
// SecResearcher (per-section draft writer)
// =============================================================================
//
// Original: gpt_researcher/prompts.py::generate_report_prompt
// (used in subtopic_report mode where the section title is
// the "question" and the gathered context is the
// research_data).

// SecResearcherSystemPrompt is the system prompt for the
// per-section draft writer. Aligned with gpt-researcher's
// generate_report_prompt.
const SecResearcherSystemPrompt = `You are a research writer. Your sole purpose is to write a well-written research report about a topic based on research findings and information.
`

// SecResearcherUserPrompt builds the user prompt.
//
// Mirrors the structure of gpt-researcher's
// generate_report_prompt: information block, query, APA
// in-text citation, today date, markdown structure,
// language.
func SecResearcherUserPrompt(section, materials string, totalWords int) string {
	if totalWords <= 0 {
		totalWords = 1000
	}
	return fmt.Sprintf(`Information: "%s"
---
Using the above information, answer the following query or task: "%s" in a detailed report --
The report should focus on the answer to the query, should be well structured, informative, in-depth, and comprehensive, with facts and numbers if available and at least %d words.
You should strive to write the report as long as you can using all relevant and necessary information provided.

Please follow all of the following guidelines in your report:
- You MUST determine your own concrete and valid opinion based on the given information. Do NOT defer to general and meaningless conclusions.
- You MUST write the report with markdown syntax and APA format.
- Structure your report with clear markdown headers: use # for the main title, ## for major sections, and ### for subsections.
- Use markdown tables when presenting structured data or comparisons to enhance readability.
- You MUST prioritize the relevance, reliability, and significance of the sources you use. Choose trusted sources over less reliable ones.
- You must also prioritize new articles over older articles if the source can be trusted.
- You MUST NOT include a table of contents, but DO include proper markdown headers (# ## ###) to structure your report clearly.
- Use in-text citation references in APA format and make it with markdown hyperlink placed at the end of the sentence or paragraph that references them like this: ([in-text citation](url)).
- Don't forget to add a reference list at the end of the report in APA format and full url links without hyperlinks.
- You MUST write all used source urls at the end of the report as references, and make sure to not add duplicated sources, but only one reference for each.
- Every url should be hyperlinked: [url website](url)
- Additionally, you MUST include hyperlinks to the relevant URLs wherever they are referenced in the report.
- eg: Author, A. A. (Year, Month Date). Title of web page. Website Name. [url website](url)
Please do your best, this is very important to my career.
Assume that the current date is today.
Write the report in Chinese.
`, materials, section, totalWords)
}

// =============================================================================
// Reviewer
// =============================================================================
//
// Original: multi_agents/agents/reviewer.py
//   TEMPLATE = "You are an expert research article reviewer.
//   Your goal is to review research drafts and provide
//   feedback to the reviser only based on specific guidelines."
//
// KEY BEHAVIOR: the reviewer returns the LITERAL STRING
// "None" to mean "accept". Anything else is treated as revise
// notes. The reviewer prompt has TWO modes:
//   - First review (no revision_notes): aggressive, ask for
//     full revision if guidelines not met.
//   - Subsequent review (with revision_notes): SOFT — only
//     ask for critical fixes, since the reviser has already
//     worked on the previous notes.

// ReviewerSystemPrompt is the system prompt for the Reviewer.
const ReviewerSystemPrompt = `You are an expert research article reviewer. Your goal is to review research drafts and provide feedback to the reviser only based on specific guidelines.
`

// ReviewerUserPrompt builds the user prompt. Mirrors
// gpt-researcher's review_prompt, including the conditional
// "soft" second-review prompt.
func ReviewerUserPrompt(guidelines, draft, revisionNotes string) string {
	soft := ""
	if revisionNotes != "" {
		soft = fmt.Sprintf(
			"The reviser has already revised the draft based on your previous review notes with the following feedback:\n%s\n\nPlease provide additional feedback ONLY if critical since the reviser has already made changes based on your previous feedback.\nIf you think the article is sufficient or that non critical revisions are required, please aim to return None.\n",
			revisionNotes,
		)
	}
	return fmt.Sprintf(`You have been tasked with reviewing the draft which was written by a non-expert based on specific guidelines.
Please accept the draft if it is good enough to publish, or send it for revision, along with your notes to guide the revision.
If not all of the guideline criteria are met, you should send appropriate revision notes.
If the draft meets all the guidelines, please return None.
%s
Guidelines: %s
Draft: %s
`, soft, guidelines, draft)
}

// =============================================================================
// Reviser
// =============================================================================
//
// Original: multi_agents/agents/reviser.py
//   sample_revision_notes = JSON {draft: {...}, revision_notes: "..."}
//   system: "You are an expert writer. Your goal is to revise
//   drafts based on reviewer notes."

// ReviserSystemPrompt is the system prompt for the Reviser.
const ReviserSystemPrompt = `You are an expert writer. Your goal is to revise drafts based on reviewer notes.
`

// ReviserSystemPromptSampleSchema is the sample JSON shape the
// reviser must return. Mirrors sample_revision_notes from
// gpt-researcher (the draft key is the section title, the
// value is the revised draft text).
const ReviserSystemPromptSampleSchema = `
{
  "draft": { 
    "<section title>": "The revised draft that you are submitting for review"
  },
  "revision_notes": "Your message to the reviewer about the changes you made to the draft based on their feedback"
}
`

// ReviserUserPrompt builds the user prompt.
func ReviserUserPrompt(section, draft, reviewNotes string) string {
	return fmt.Sprintf(`Draft:
%s
Reviewer's notes:
%s

You have been tasked by your reviewer with revising the following draft, which was written by a non-expert.
If you decide to follow the reviewer's notes, please write a new draft and make sure to address all of the points they raised.
Please keep all other aspects of the draft the same.
You MUST return nothing but a JSON in the following format:
%s
`, draft, reviewNotes, ReviserSystemPromptSampleSchema)
}

// =============================================================================
// Writer
// =============================================================================
//
// Original: multi_agents/agents/writer.py
//   sample_json = JSON {table_of_contents, introduction, conclusion, sources}
//   system: "You are a research writer. Your sole purpose is
//   to write a well-written research reports about a topic
//   based on research findings and information."
//
// KEY INSIGHT (which I missed in my first version): the Writer
// does NOT generate the body sections — those are the
// per-section drafts. The Writer generates the LAYOUT: ToC,
// introduction, conclusion, and the sources list. The body
// comes from the per-section drafts assembled around this
// layout.

// WriterSystemPrompt is the system prompt for the Writer.
const WriterSystemPrompt = `You are a research writer. Your sole purpose is to write a well-written research reports about a topic based on research findings and information.
`

// WriterSystemPromptSampleSchema is the JSON shape the
// Writer must return. Mirrors sample_json from
// gpt-researcher.
const WriterSystemPromptSampleSchema = `
{
  "table_of_contents": A table of contents in markdown syntax (using '-') based on the research headers and subheaders,
  "introduction": An indepth introduction to the topic in markdown syntax and hyperlink references to relevant sources,
  "conclusion": A conclusion to the entire research based on all research data in markdown syntax and hyperlink references to relevant sources,
  "sources": A list with strings of all used source links in the entire research data in markdown syntax and apa citation format. For example: ['-  Title, year, Author [source url](source url)', ...]
}
`

// WriterUserPrompt builds the user prompt.
func WriterUserPrompt(query, title, researchDataJSON string) string {
	return fmt.Sprintf(`Today's date is today.
Query or Topic: %s
Research data: %s

Your task is to write an in depth, well written and detailed introduction and conclusion to the research report based on the provided research data.

IMPORTANT formatting rules:
- The introduction and conclusion MUST be written in rich markdown (use **bold**, bullet lists, blockquotes where appropriate).
- Do NOT start the introduction or conclusion with a top-level heading (# or ##) — the system wraps each section with its own heading.
- You MAY use sub-headings (### or ####) inside the introduction/conclusion if it helps structure.
- You MUST include any relevant sources as markdown hyperlinks inline, e.g. 'This is a sample text. ([url website](url))'.

You MUST return nothing but a JSON in the following format (without json markdown):
%s
`, query, researchDataJSON, WriterSystemPromptSampleSchema)
}
