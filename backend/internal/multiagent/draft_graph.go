// Package multiagent — draft_graph.go builds the per-section
// self-correction subgraph. This is the heart of the
// multi-agent value: the same draft is reviewed and (if
// needed) revised up to MaxDraftRevisions times before being
// accepted.
//
// Topology:
//
//	START → sec_researcher → reviewer ─(accept)─→ END
//	                            │
//	                            └─(revise)──→ reviser → reviewer
//	                                              ↑      │
//	                                              └──────┘  (cycle)
//
// sec_researcher: writes the initial section draft from
//
//	the gathered material (LLM call).
//
// reviewer:       evaluates the draft against the review
//
//	criteria (LLM call). Returns acceptSentinel
//	(= "") or revise notes.
//
// reviser:        rewrites the draft per the review notes
//
//	(LLM call). Loops back to reviewer.
//
// The cycle is bounded by MaxDraftRevisions (default 3) and
// guarded by compose.WithMaxRunSteps at the outer compile
// site (graph.go).
//
// The subgraph's I/O is DraftState. The state is per-section:
// each section's outer Researcher invocation creates its own
// DraftState and runs this subgraph.
package multiagent

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/compose"

	"z-research/backend/internal/config"
	"z-research/backend/internal/llm"
	"z-research/backend/internal/researcher"
)

// Draft-graph node keys.
const (
	DraftNodeSecResearcher = "sec_researcher"
	DraftNodeReviewer      = "reviewer"
	DraftNodeReviser       = "reviser"

	DraftBranchAccept = compose.END
	DraftBranchRevise = DraftNodeReviser
)

// BuildDraftGraph assembles the per-section self-correction
// subgraph. The returned graph is compiled once at outer
// graph build time and invoked per section with a fresh
// DraftState.
func BuildDraftGraph(
	cfg *config.Config,
	llmClient *llm.LLM,
	inner *researcher.Researcher,
) (*compose.Graph[DraftState, DraftState], error) {
	g := compose.NewGraph[DraftState, DraftState]()

	// --- Node: sec_researcher (writes initial draft) ---
	secResNode := compose.InvokableLambda(
		func(ctx context.Context, in DraftState) (DraftState, error) {
			// First run: in has Section + MaterialBlock.
			// Subsequent runs (post-cycle) only carry the
			// updated Draft; in that case we just return
			// it unchanged. The check is "has the draft
			// already been written?".
			if in.Draft != "" {
				return in, nil
			}
			if in.MaterialBlock == "" {
				return in, fmt.Errorf("sec_researcher: empty material block for section %q",
					in.Section)
			}
			draft, err := WriteSectionDraft(ctx, llmClient, in.Section, in.MaterialBlock, cfg.TotalWords)
			if err != nil {
				return in, err
			}
			in.Draft = draft
			return in, nil
		},
	)
	if err := g.AddLambdaNode(DraftNodeSecResearcher, secResNode, compose.WithNodeName(DraftNodeSecResearcher)); err != nil {
		return nil, fmt.Errorf("add sec_researcher: %w", err)
	}

	// --- Node: reviewer ---
	reviewerNode := compose.InvokableLambda(
		func(ctx context.Context, in DraftState) (DraftState, error) {
			// Mirrors gpt-researcher reviewer.py: guidelines
			// come from task config; revisionNotes is empty
			// on the first pass and carries the reviser's
			// last notes on subsequent passes (soft prompt).
			accept, notes, err := ReviewDraft(ctx, llmClient, in.Section, in.Draft,
				strings.Join(defaultReviewerGuidelines, "\n- "),
				"", // soft-prompt path disabled in this MVP; see ReviseDraft for storage
			)
			if err != nil {
				return in, err
			}
			if accept {
				in.ReviewNotes = acceptSentinel
			} else {
				in.ReviewNotes = notes
			}
			return in, nil
		},
	)
	if err := g.AddLambdaNode(DraftNodeReviewer, reviewerNode, compose.WithNodeName(DraftNodeReviewer)); err != nil {
		return nil, fmt.Errorf("add reviewer: %w", err)
	}

	// --- Node: reviser ---
	reviserNode := compose.InvokableLambda(
		func(ctx context.Context, in DraftState) (DraftState, error) {
			if in.RevisionCount >= cfg.MaxDraftRevisions {
				// Cap reached; force-accept by
				// emitting the accept sentinel as
				// the next review's "notes".
				in.ReviewNotes = acceptSentinel
				return in, nil
			}
			newDraft, _, err := ReviseDraft(ctx, llmClient, in.Section, in.Draft, in.ReviewNotes)
			if err != nil {
				return in, err
			}
			in.Draft = newDraft
			in.RevisionCount++
			return in, nil
		},
	)
	if err := g.AddLambdaNode(DraftNodeReviser, reviserNode, compose.WithNodeName(DraftNodeReviser)); err != nil {
		return nil, fmt.Errorf("add reviser: %w", err)
	}

	// --- Branch on reviewer: accept→END, revise→reviser ---
	reviewerBranch := compose.NewGraphBranch(
		func(_ context.Context, in DraftState) (string, error) {
			if in.ReviewNotes == acceptSentinel {
				return DraftBranchAccept, nil
			}
			return DraftBranchRevise, nil
		},
		map[string]bool{DraftBranchAccept: true, DraftBranchRevise: true},
	)
	if err := g.AddBranch(DraftNodeReviewer, reviewerBranch); err != nil {
		return nil, fmt.Errorf("add reviewer branch: %w", err)
	}

	// --- Edges ---
	if err := g.AddEdge(compose.START, DraftNodeSecResearcher); err != nil {
		return nil, fmt.Errorf("edge START->sec_researcher: %w", err)
	}
	if err := g.AddEdge(DraftNodeSecResearcher, DraftNodeReviewer); err != nil {
		return nil, fmt.Errorf("edge sec_researcher->reviewer: %w", err)
	}
	// reviewer → END is established by the branch's
	// "accept" arm. The "revise" arm routes to reviser.
	if err := g.AddEdge(DraftNodeReviser, DraftNodeReviewer); err != nil {
		return nil, fmt.Errorf("edge reviser->reviewer: %w", err)
	}

	return g, nil
}
