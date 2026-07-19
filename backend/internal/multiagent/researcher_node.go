// Package multiagent — researcher_node.go implements the
// outer "researcher" graph node. This node fans out to one
// per-section subgraph (draft_graph.go) for every section in
// the plan, runs them concurrently, and collects the resulting
// drafts into ResearchState.Drafts.
//
// Each per-section subgraph is the sec_researcher → reviewer ↔
// reviser self-correction loop. The loop is the multi-agent
// value: the same draft is reviewed and (if needed) revised
// up to MaxDraftRevisions times before being accepted.
//
// Section-level material gathering (search → fetch → compress)
// reuses the existing researcher.Researcher.Conduct method
// from the single-agent engine. The Conduct call is invoked
// once per section, treating the section title as the query.
// This means the multi-agent flow does N parallel research
// runs (one per section) where N ≤ MaxSections.
package multiagent

import (
	"context"
	"fmt"
	"sync"

	"github.com/cloudwego/eino/compose"

	"z-research/backend/internal/collection"
	"z-research/backend/internal/config"
	"z-research/backend/internal/llm"
	"z-research/backend/internal/researcher"
)

// buildResearcherNode returns the lambda for the outer
// "researcher" node. The lambda:
//
//  1. Reads ResearchState.Sections.
//  2. For each section, compiles the per-section subgraph
//     (draft_graph.go) and invokes it with a DraftState.
//  3. Collects drafts + sources into ResearchState.
//
// The per-section subgraphs run concurrently (errgroup). On
// any section failure, the run aborts.
func buildResearcherNode(
	cfg *config.Config,
	llmClient *llm.LLM,
	inner *researcher.Researcher,
) (*compose.Lambda, error) {
	// Per-section subgraphs are stateful: they share no
	// per-run state, only their own DraftState. We compile
	// a single draftGraph template and clone state per
	// invocation by passing a fresh DraftState.
	draftGraph, err := BuildDraftGraph(cfg, llmClient, inner)
	if err != nil {
		return nil, fmt.Errorf("build draft graph: %w", err)
	}

	// Compile once; the same compiled runnable is invoked
	// per section (with a fresh DraftState in the input).
	ctx0 := context.Background()
	draftRunnable, err := draftGraph.Compile(ctx0,
		compose.WithMaxRunSteps(cfg.MaxRunSteps),
	)
	if err != nil {
		return nil, fmt.Errorf("compile draft graph: %w", err)
	}

	// Hold compiled runnable in closure. The lambda
	// captures draftRunnable, cfg, llmClient, inner.
	return compose.InvokableLambda(
		func(ctx context.Context, _ string) (string, error) {
			// Read sections from state.
			var sections []string
			_ = compose.ProcessState[*ResearchState](ctx, func(_ context.Context, s *ResearchState) error {
				sections = s.Sections
				return nil
			})
			if len(sections) == 0 {
				return "", fmt.Errorf("researcher node: no sections in state")
			}

			// Per-section material gathering. We
			// run this concurrently with errgroup.
			type secResult struct {
				idx      int
				section  string
				material string
				sources  []researcher.Source
				err      error
			}
			results := make([]secResult, len(sections))
			var wg sync.WaitGroup
			wg.Add(len(sections))
			for i, sec := range sections {
				i, sec := i, sec
				go func() {
					defer wg.Done()
					mat, src, err := gatherSectionMaterial(ctx, inner, sec)
					results[i] = secResult{idx: i, section: sec, material: mat, sources: src, err: err}
				}()
			}
			wg.Wait()

			// Check for errors and collect.
			drafts := make([]string, len(sections))
			var allSources []researcher.Source
			for _, r := range results {
				if r.err != nil {
					return "", fmt.Errorf("section %d (%q) gather: %w",
						r.idx, r.section, r.err)
				}
				// Write the initial draft (sec_researcher)
				// and run the review↔revise loop via the
				// inner subgraph.
				ds := DraftState{
					Section:       r.section,
					MaterialBlock: r.material, // see below; we add this field
				}
				out, err := draftRunnable.Invoke(ctx, ds)
				if err != nil {
					return "", fmt.Errorf("section %d (%q) draft graph: %w",
						r.idx, r.section, err)
				}
				drafts[r.idx] = out.Draft
				allSources = append(allSources, r.sources...)
			}

			// Deduplicate sources by URL (the inner
			// Conduct already does this internally, but
			// sections can share the same source).
			deduped := dedupSources(allSources)

			// Write back to state.
			_ = compose.ProcessState[*ResearchState](ctx, func(_ context.Context, s *ResearchState) error {
				s.Drafts = drafts
				s.Sources = deduped
				return nil
			})

			// Return value is a summary used by the
			// writer node's data flow; the actual draft
			// text comes from state.
			return fmt.Sprintf("%d sections researched", len(sections)), nil
		},
	), nil
}

// gatherSectionMaterial runs the single-agent
// researcher.Researcher.Conduct pipeline for one section.
// Returns the numbered material text + the source list
// (with citation numbers). The material is in the same
// "Source: [n] url\nTitle: ...\nContent: ..." format that
// Conduct produces.
func gatherSectionMaterial(
	ctx context.Context,
	inner *researcher.Researcher,
	section string,
) (material string, sources []researcher.Source, err error) {
	res, err := inner.Conduct(ctx, section, nil)
	if err != nil {
		return "", nil, err
	}
	return res.Context, res.Sources, nil
}

// dedupSources 已迁移至 internal/collection 包。
// 这里保留薄封装：多智能体场景需要"边去重边重编号 1..N"的语义，
// 等价于 collection.Merge(nil, in)（合并到空基线后重新连续编号）。
func dedupSources(in []researcher.Source) []researcher.Source {
	return collection.Merge(nil, in)
}
