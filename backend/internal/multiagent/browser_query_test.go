// Package multiagent — browser_query_test.go is the
// regression test for the bug where the Browser node
// (which runs FIRST in the graph, before Planner) could
// not see the per-run injected query because the state
// injection pre-handler was only attached to Planner.
//
// Symptom: MiniMax-M3 returned "<think> The user wants me
// to generate 3 search terms for a research topic, but
// the research topic is empty/missing" — because
// SubQueryUserPrompt received an empty query.
//
// Fix: attach injectInitialStatePreHandler to BOTH Browser
// and Planner nodes. This test asserts the Browser node's
// lambda sees a non-empty state.Query after the pre-handler
// runs.
package multiagent

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/compose"
)

// TestBrowserNode_ReadsInjectedQuery builds a minimal graph
// that mirrors the production topology (START → Browser →
// END) with the SAME injectInitialStatePreHandler attached
// to Browser, and asserts the Browser lambda sees the
// query injected via WithInitialState.
func TestBrowserNode_ReadsInjectedQuery(t *testing.T) {
	ctx := context.Background()

	g := compose.NewGraph[string, string](
		compose.WithGenLocalState(func(ctx context.Context) *ResearchState {
			return &ResearchState{}
		}),
	)

	// Browser lambda mirrors the production one: reads
	// state.Query. We capture what it saw.
	var seenQuery string
	browser := compose.InvokableLambda(func(ctx context.Context, _ string) (string, error) {
		_ = compose.ProcessState[*ResearchState](ctx, func(_ context.Context, s *ResearchState) error {
			seenQuery = s.Query
			return nil
		})
		return "browser done", nil
	})
	if err := g.AddLambdaNode(NodeBrowser, browser,
		compose.WithNodeName(NodeBrowser),
		// THE FIX: same pre-handler as production.
		compose.WithStatePreHandler(injectInitialStatePreHandler),
	); err != nil {
		t.Fatal(err)
	}
	if err := g.AddEdge(compose.START, NodeBrowser); err != nil {
		t.Fatal(err)
	}
	if err := g.AddEdge(NodeBrowser, compose.END); err != nil {
		t.Fatal(err)
	}

	runnable, err := g.Compile(ctx, compose.WithMaxRunSteps(10))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	// Inject a query via the context (mirrors Engine.Run).
	const wantQuery = "固态电池 2026 最新进展"
	ctx = WithInitialState(ctx, &ResearchState{Query: wantQuery})

	if _, err := runnable.Invoke(ctx, "ignored"); err != nil {
		t.Fatalf("invoke: %v", err)
	}

	if seenQuery != wantQuery {
		t.Fatalf("Browser node saw state.Query = %q, want %q\n"+
			"  This means the injectInitialStatePreHandler is NOT attached\n"+
			"  to the Browser node. Without it, Browser runs before Planner\n"+
			"  and sees an empty query, causing 'research topic is empty' errors.",
			seenQuery, wantQuery)
	}
}

// TestBrowserNode_QueryEmpty_WithoutPreHandler is the
// inverse: WITHOUT the pre-handler, Browser sees an empty
// query. This documents the original bug for future
// reference.
func TestBrowserNode_QueryEmpty_WithoutPreHandler(t *testing.T) {
	ctx := context.Background()
	g := compose.NewGraph[string, string](
		compose.WithGenLocalState(func(ctx context.Context) *ResearchState {
			return &ResearchState{}
		}),
	)
	var seenQuery string
	browser := compose.InvokableLambda(func(ctx context.Context, _ string) (string, error) {
		_ = compose.ProcessState[*ResearchState](ctx, func(_ context.Context, s *ResearchState) error {
			seenQuery = s.Query
			return nil
		})
		return "done", nil
	})
	// NOTE: no WithStatePreHandler here — this is the
	// buggy configuration.
	if err := g.AddLambdaNode(NodeBrowser, browser, compose.WithNodeName(NodeBrowser)); err != nil {
		t.Fatal(err)
	}
	if err := g.AddEdge(compose.START, NodeBrowser); err != nil {
		t.Fatal(err)
	}
	if err := g.AddEdge(NodeBrowser, compose.END); err != nil {
		t.Fatal(err)
	}
	runnable, err := g.Compile(ctx, compose.WithMaxRunSteps(10))
	if err != nil {
		t.Fatal(err)
	}
	ctx = WithInitialState(ctx, &ResearchState{Query: "should be invisible without pre-handler"})
	if _, err := runnable.Invoke(ctx, "ignored"); err != nil {
		t.Fatal(err)
	}
	// Without pre-handler, the Browser lambda sees the
	// zero-value state (empty Query). This documents the
	// bug we fixed.
	if seenQuery != "" {
		t.Fatalf("expected empty query without pre-handler, got %q (pre-handler may now be auto-applied?)", seenQuery)
	}
	_ = strings.TrimSpace // keep import used if assertions shrink
}
