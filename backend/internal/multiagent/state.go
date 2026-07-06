// Package multiagent implements a multi-agent research engine
// built on CloudWeGo Eino's compose graph. The architecture
// mirrors gpt-researcher's STORM-inspired pipeline (Chief
// Editor → Planner → Researcher → Reviewer ↔ Reviser → Writer)
// and re-uses the existing single-agent Engine (researcher
// package) for the "collect material" sub-step inside each
// Researcher node.
//
// This file defines the shared state types and the
// control-flow sentinels used across the graph nodes. There
// are no reducers — every field is plain last-write-wins,
// matching gpt-researcher's ResearchState/DraftState semantics.
package multiagent

import (
	"context"

	"z-research/backend/internal/researcher"
)

// acceptSentinel is the non-empty string a role returns to
// signal "accept" on a draft. We cannot return the empty
// string because the Eino Pregel engine treats "" as "no data
// produced" and the graph terminates with an empty output.
//
// IMPORTANT: every "accept" path in the multi-agent graph
// (planner-accepted-by-human, reviewer-accepted-draft, etc.)
// MUST return this constant rather than the empty string.
// This convention is enforced by the smoke test
// (TestEinoGraph_ConditionalBranchAndCycle).
const acceptSentinel = "__ACCEPT__"

// reviseSentinel is the non-empty string a role returns to
// signal "revise" when the role does not have any LLM-generated
// notes (e.g. test stubs). Production code returns the LLM's
// free-form review notes instead; the branch condition only
// cares that the value differs from acceptSentinel.
const reviseSentinel = "__REVISE__"

// ResearchState is the shared per-run state of the outer
// multi-agent graph (planner → human_review → researcher →
// writer). The graph's I/O type is the research plan + final
// report; this struct lives in the per-run state slot
// allocated by WithGenLocalState.
type ResearchState struct {
	// Query is the user's original research question, set
	// once at graph start and never modified.
	Query string

	// Title is the report title, set by the Planner.
	Title string

	// Sections is the section list, set by the Planner and
	// (optionally) revised by the human reviewer.
	Sections []string

	// Drafts is a parallel array: Drafts[i] is the final
	// draft for Sections[i] after the per-section review
	// loop terminates. Empty if the per-section loop has
	// not yet run for that section.
	Drafts []string

	// Sources aggregated from every per-section researcher
	// call. Section i's sources are appended after
	// Drafts[i] is finalized. FinalReport at the very end
	// dedups by URL.
	Sources []researcher.Source

	// HumanFeedback carries the human reviewer's free-form
	// response when they choose to revise the plan. Empty
	// when accepted. Routed by the human_review branch.
	HumanFeedback string

	// PlanRevisions counts how many times the human has
	// revised the plan. Capped by MaxPlanRevisions in
	// config. After the cap, the branch forces "accept".
	PlanRevisions int

	// Report is the final markdown report, set by the
	// Writer node. The graph's I/O type is this string.
	Report string

	// CurrentSectionIndex, while the graph is running, holds
	// the index of the section being processed. Used by the
	// Researcher node to fan out to per-section subgraphs
	// and by the progress callback to group messages.
	CurrentSectionIndex int

	// InitialResearch is the markdown summary produced
	// by the Browser node (which runs a full single-agent
	// research pass before the Planner). The Planner
	// reads this to make an informed outline.
	// Mirrors gpt-researcher's "initial_research" field
	// in ResearchState.
	InitialResearch string

	// Per-run configuration. Injected from researcher.Options
	// before graph.Invoke is called. The graph nodes read
	// these via compose.ProcessState.

	// MaxSections overrides cfg.MaxSections for this run.
	MaxSections int

	// MaxPlanRevisions overrides cfg.MaxPlanRevisions.
	MaxPlanRevisions int

	// EnableHITL overrides cfg.EnableHITL.
	EnableHITL bool

	// HumanFeedbackFn is the callback the human_review
	// node invokes when EnableHITL is true. nil =
	// auto-accept.
	HumanFeedbackFn func(ctx context.Context, plan researcher.HumanReviewPlan) (string, error)

	// OnReportChunk streams report chunks to the WebSocket
	// handler. Optional (nil = no streaming).
	OnReportChunk func(chunk, accu string)

	// OnProgress is the per-stage progress callback
	// (planner, human, researcher per-section, writer).
	// Mirrors the single-agent Engine.EventFn.
	OnProgress func(stage, message string)
}

// DraftState is the per-section state inside the inner
// reviewer↔reviser loop. Each section gets its own DraftState
// via the per-section subgraph invocation.
type DraftState struct {
	// Section is the section title this draft belongs to.
	Section string

	// MaterialBlock is the numbered "Source: [n] url\n
	// Title: ...\nContent: ..." text produced by
	// researcher.Researcher.Conduct. The sec_researcher
	// node consumes this to write the initial draft.
	MaterialBlock string

	// Draft is the current section draft text. The
	// sec_researcher node sets it initially; the reviser
	// overwrites it after each revise round.
	Draft string

	// ReviewNotes is the most recent reviewer's feedback.
	// Empty when accepted. The branch routes on whether
	// this equals acceptSentinel.
	ReviewNotes string

	// RevisionCount is how many times reviser has been
	// invoked for this section. Capped by
	// MaxDraftRevisions in config.
	RevisionCount int
}
