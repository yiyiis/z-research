// Package multiagent provides a multi-agent research engine built on
// CloudWeGo Eino's compose graph. This file is a smoke test that
// validates the two load-bearing capabilities we rely on for the
// planner→reviewer self-correction loop:
//
//  1. Conditional branching (compose.NewGraphBranch + AddBranch):
//     the human-review node's output is inspected at runtime to
//     choose the next node (accept → researcher, revise → planner
//     back-edge). Same primitive as gpt-researcher's conditional
//     edges on the human-feedback node.
//
//  2. A real cycle in the graph (reviewer → reviser → reviewer …)
//     that terminates when the reviewer signals accept. The cycle
//     is guarded by compose.WithMaxRunSteps so a misbehaving
//     reviewer can never run away.
//
//  3. Shared state across nodes via compose.WithGenLocalState +
//     compose.ProcessState — the recommended, concurrency-safe
//     way to accumulate per-run data inside a graph.
//
// If this test passes on a given Eino version, the multi-agent
// architecture described in docs/architecture.md is feasible. If it
// fails, the plan has to change (fall back to hand-written
// orchestration, see plan §"风险与缓解").
package multiagent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/compose"
)

// loopState is the shared per-run state for the smoke test graph.
// It is mutated by every node through compose.ProcessState and is
// also used by the test to communicate driver flags into the graph.
type loopState struct {
	mu sync.Mutex

	// steps counts how many times the review node has executed.
	steps int

	// humanInvoked counts how many times the human node has run;
	// used to drive replanOnce behavior.
	humanInvoked int

	// plannerInvoked counts how many times the planner node has
	// run; used by runSmokeWithPlannerReplan to decide the
	// planner's next output tag.
	plannerInvoked int

	// humanShouldAccept, when true, makes the human node route
	// to the accept target via the conditional branch.
	humanShouldAccept bool

	// replanOnce, when true, flips humanShouldAccept to true
	// after the first human invocation (mimics "human revises
	// once, then accepts").
	replanOnce bool

	// acceptAfter makes the reviewer say "revise" acceptAfter
	// times, then accept (empty string). 0 = accept immediately.
	acceptAfter int

	// log is the ordered sequence of node invocations. Used by
	// test assertions to verify the path the engine took.
	log []string
}

func (s *loopState) appendLog(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, name)
}

func (s *loopState) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.log))
	copy(out, s.log)
	return out
}

// TestEinoGraph_ConditionalBranchAndCycle is the go/no-go gate for
// the entire multi-agent refactor. It builds a minimal graph that
// mirrors the real planner→human-review→(accept|revise)→end
// topology plus a real review↔revise cycle, runs it with fake
// behavior (pure function lambdas, no LLM), and asserts the
// engine actually traverses the paths we expect.
//
// The graph under test:
//
//	START → planner → human ──accept──→ researcher → reviewer
//	          ▲            │                       │   │
//	          └─revise─────┘                       ▼   ▼
//	                                          reviser  accept→END
//	                                            │
//	                                            └──→ reviewer (cycle)
//
// All "data" is encoded in the input string. The planner tags its
// output with a prefix the human reads, etc. In production each
// node will instead read/write loopState directly.
func TestEinoGraph_ConditionalBranchAndCycle(t *testing.T) {
	t.Run("accept path - human accepts, cycle runs zero times", func(t *testing.T) {
		log, err := runSmoke(&smokeDriver{
			humanShouldAccept: true,
			acceptAfter:       0,
		})
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		// Expect: planner → human → researcher → reviewer (accept) → END.
		// No reviser. No replan loop.
		assertLog(t, log, []string{"planner", "human", "researcher", "reviewer"})
		if count(log, "reviewer") != 1 {
			t.Errorf("expected reviewer to run once, got %d", count(log, "reviewer"))
		}
		if count(log, "reviser") != 0 {
			t.Errorf("expected reviser to run zero times, got %d", count(log, "reviser"))
		}
		if count(log, "human") != 1 {
			t.Errorf("expected human to run once, got %d", count(log, "human"))
		}
	})

	t.Run("revise path - human revises, plan replans once then accepts", func(t *testing.T) {
		// This case verifies the conditional branch can re-route
		// from human back to planner, and that planner gets a
		// second chance to produce a different output. The
		// second planner invocation now tags its output with
		// "ACCEPT" suffix; the human branch reads that tag to
		// route to "researcher" instead of "planner" again.
		log, err := runSmokeWithPlannerReplan()
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		// Expect: planner#1 → human#1 → (revise) → planner#2 →
		// human#2 → (accept) → researcher → reviewer (accept)
		// → END. (Log strings carry invocation suffixes for
		// debuggability; we match prefixes.)
		assertLogPrefix(t, log, []string{
			"planner", "human", "planner", "human", "researcher", "reviewer",
		})
		if countPrefix(log, "planner") != 2 {
			t.Errorf("expected planner to run twice, got %d; log=%v",
				countPrefix(log, "planner"), log)
		}
		if countPrefix(log, "human") != 2 {
			t.Errorf("expected human to run twice, got %d; log=%v",
				countPrefix(log, "human"), log)
		}
	})

	t.Run("cycle path - reviewer revises twice then accepts", func(t *testing.T) {
		log, err := runSmoke(&smokeDriver{
			humanShouldAccept: true,
			acceptAfter:       2, // 2 revisions then accept
		})
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		// Expect: planner → human → researcher → reviewer → reviser →
		// reviewer → reviser → reviewer (accept) → END.
		// acceptAfter=2 → reviewer runs 2+1=3 times, reviser runs 2.
		assertLog(t, log, []string{
			"planner", "human", "researcher",
			"reviewer", "reviser", "reviewer", "reviser", "reviewer",
		})
		if got := count(log, "reviewer"); got != 3 {
			t.Errorf("expected reviewer to run 3 times, got %d; log=%v", got, log)
		}
		if got := count(log, "reviser"); got != 2 {
			t.Errorf("expected reviser to run 2 times, got %d; log=%v", got, log)
		}
	})

	t.Run("cycle guard - max steps terminates runaway reviewer", func(t *testing.T) {
		// acceptAfter=9999 + a max-steps limit. The graph must
		// terminate even though the reviewer would loop forever.
		_, err := runSmoke(&smokeDriver{
			humanShouldAccept: true,
			acceptAfter:       9999,
		})
		if err == nil {
			t.Fatalf("expected max-steps error, got nil")
		}
		if !strings.Contains(strings.ToLower(err.Error()), "max steps") {
			t.Fatalf("expected max steps error, got: %v", err)
		}
	})
}

// acceptSentinel is defined in state.go (same package).

// smokeDriver sets the test-only fields on the shared loopState.
type smokeDriver struct {
	humanShouldAccept bool
	replanOnce        bool
	acceptAfter       int
}

// runSmokeWithPlannerReplan builds a graph variant that
// demonstrates the "replan once, then accept" path. The graph is
// identical to runSmoke's except:
//
//   - The human branch inspects the planner's *output* (tagged
//     with "ACCEPT:" or "REVISE:") to decide the next node.
//     This mirrors the real impl where the planner embeds an
//     intent flag in its section list.
//   - The first planner invocation tags its output "REVISE:",
//     causing the branch to route back to planner. The second
//     invocation tags its output "ACCEPT:", routing to
//     researcher.
//
// This pattern decouples the replan decision from the human
// node's identity, which is more faithful to the production
// design and avoids the "branch decision was made before state
// was updated" race we hit in the simpler runSmoke driver.
func runSmokeWithPlannerReplan() ([]string, error) {
	ctx := context.Background()

	stateGen := func(ctx context.Context) *loopState {
		currentLogState = &loopState{acceptAfter: 0}
		return currentLogState
	}

	g := compose.NewGraph[string, string](
		compose.WithGenLocalState(stateGen),
	)

	planner := compose.InvokableLambda(func(ctx context.Context, in string) (string, error) {
		var accept bool
		var invoked int
		_ = compose.ProcessState[*loopState](ctx, func(_ context.Context, s *loopState) error {
			s.appendLog("planner")
			// First planner invocation: output a "REVISE:"
			// tag so the human branch routes back. Second
			// invocation: "ACCEPT:" so we proceed to
			// researcher.
			s.plannerInvoked++
			invoked = s.plannerInvoked
			accept = s.plannerInvoked >= 2
			return nil
		})
		_ = invoked
		if accept {
			return "ACCEPT:" + in, nil
		}
		return "REVISE:" + in, nil
	})
	if err := g.AddLambdaNode("planner", planner, compose.WithNodeName("planner")); err != nil {
		return nil, err
	}

	human := compose.InvokableLambda(func(ctx context.Context, in string) (string, error) {
		_ = compose.ProcessState[*loopState](ctx, func(_ context.Context, s *loopState) error {
			s.humanInvoked++
			s.appendLog(fmt.Sprintf("human#%d(in=%s)", s.humanInvoked, in))
			return nil
		})
		return in, nil
	})
	if err := g.AddLambdaNode("human", human, compose.WithNodeName("human")); err != nil {
		return nil, err
	}

	researcher := compose.InvokableLambda(func(ctx context.Context, in string) (string, error) {
		_ = compose.ProcessState[*loopState](ctx, func(_ context.Context, s *loopState) error {
			s.appendLog(fmt.Sprintf("researcher(in=%s)", in))
			return nil
		})
		return "draft:" + in, nil
	})
	if err := g.AddLambdaNode("researcher", researcher, compose.WithNodeName("researcher")); err != nil {
		return nil, err
	}

	reviewer := compose.InvokableLambda(func(ctx context.Context, _ string) (string, error) {
		_ = compose.ProcessState[*loopState](ctx, func(_ context.Context, s *loopState) error {
			s.steps++
			s.appendLog("reviewer")
			return nil
		})
		// acceptAfter=0 means always accept; the test
		// verifies only the human-replan path here, not the
		// reviewer cycle.
		return acceptSentinel, nil
	})
	if err := g.AddLambdaNode("reviewer", reviewer, compose.WithNodeName("reviewer")); err != nil {
		return nil, err
	}

	// reviser is a no-op node in this replan test. The
	// reviewer always returns the accept sentinel (acceptAfter
	// is 0), so reviser is never reached. But the reviewer
	// branch's endNodes map must reference an existing node,
	// so we still register it.
	reviser := compose.InvokableLambda(func(ctx context.Context, in string) (string, error) {
		return in, nil
	})
	if err := g.AddLambdaNode("reviser", reviser, compose.WithNodeName("reviser")); err != nil {
		return nil, err
	}

	// Human branch: inspect planner's output prefix.
	humanBranch := compose.NewGraphBranch(
		func(_ context.Context, in string) (string, error) {
			if len(in) >= 7 && in[:7] == "ACCEPT:" {
				return "researcher", nil
			}
			return "planner", nil
		},
		map[string]bool{"researcher": true, "planner": true},
	)
	if err := g.AddBranch("human", humanBranch); err != nil {
		return nil, err
	}

	reviewerBranch := compose.NewGraphBranch(
		func(_ context.Context, in string) (string, error) {
			if in == acceptSentinel {
				return compose.END, nil
			}
			return "reviser", nil
		},
		map[string]bool{compose.END: true, "reviser": true},
	)
	if err := g.AddBranch("reviewer", reviewerBranch); err != nil {
		return nil, err
	}

	if err := g.AddEdge(compose.START, "planner"); err != nil {
		return nil, err
	}
	if err := g.AddEdge("planner", "human"); err != nil {
		return nil, err
	}
	// NO AddEdge("human", "researcher")! The human branch
	// routes the human node's output to either "researcher"
	// (accept) or "planner" (revise, back-edge). Adding a
	// plain edge in addition to the branch causes both flows
	// to fire, producing duplicate downstream runs.
	if err := g.AddEdge("researcher", "reviewer"); err != nil {
		return nil, err
	}
	if err := g.AddEdge("reviser", "reviewer"); err != nil {
		return nil, err
	}

	runnable, err := g.Compile(ctx, compose.WithMaxRunSteps(30))
	if err != nil {
		return nil, err
	}

	out, err := runnable.Invoke(ctx, "query")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, fmt.Errorf("empty output")
	}
	return currentLogState.snapshot(), nil
}

// runSmoke builds the full graph and runs it once. Returns the
// ordered execution log. An error from Compile or Invoke means
// the test should fail (the smoke test failed).
func runSmoke(d *smokeDriver) ([]string, error) {
	ctx := context.Background()

	// The shared state. The graph constructor receives a
	// GenLocalState that creates a fresh one per run. To make
	// the log visible to the test, we also stash a pointer in a
	// package-level variable. The smoke test is single-threaded
	// by construction, so this is safe.
	stateGen := func(ctx context.Context) *loopState {
		s := &loopState{
			humanShouldAccept: d.humanShouldAccept,
			replanOnce:        d.replanOnce,
			acceptAfter:       d.acceptAfter,
		}
		currentLogState = s
		return s
	}

	g := compose.NewGraph[string, string](
		compose.WithGenLocalState(stateGen),
	)

	// Order matters in Eino:
	//   - AddBranch requires the branch's start node AND every end
	//     node in its endNodes map to be already registered via
	//     AddLambdaNode (graph.go:489 + graph.go:516).
	//   - AddEdge also requires both endpoints registered.
	// So we register every node first, then add all branches
	// and edges.

	// ---- Node: planner ----
	planner := compose.InvokableLambda(func(ctx context.Context, in string) (string, error) {
		_ = compose.ProcessState[*loopState](ctx, func(_ context.Context, s *loopState) error {
			s.appendLog("planner")
			return nil
		})
		return "plan:" + in, nil
	})
	if err := g.AddLambdaNode("planner", planner, compose.WithNodeName("planner")); err != nil {
		return nil, fmt.Errorf("add planner: %w", err)
	}

	// ---- Node: human ----
	human := compose.InvokableLambda(func(ctx context.Context, in string) (string, error) {
		_ = compose.ProcessState[*loopState](ctx, func(_ context.Context, s *loopState) error {
			s.humanInvoked++
			s.appendLog("human")
			// replanOnce: flip to accept on the 2nd invocation
			// so the graph proceeds to researcher. (The first
			// invocation already produced a revise → planner
			// back-edge via the branch.)
			if s.replanOnce && s.humanInvoked == 2 {
				s.humanShouldAccept = true
			}
			return nil
		})
		return in, nil
	})
	if err := g.AddLambdaNode("human", human, compose.WithNodeName("human")); err != nil {
		return nil, fmt.Errorf("add human: %w", err)
	}

	// ---- Node: researcher ----
	researcher := compose.InvokableLambda(func(ctx context.Context, in string) (string, error) {
		_ = compose.ProcessState[*loopState](ctx, func(_ context.Context, s *loopState) error {
			s.appendLog("researcher")
			return nil
		})
		return "draft:" + in, nil
	})
	if err := g.AddLambdaNode("researcher", researcher, compose.WithNodeName("researcher")); err != nil {
		return nil, fmt.Errorf("add researcher: %w", err)
	}

	// ---- Node: reviewer (cycle start) ----
	reviewer := compose.InvokableLambda(func(ctx context.Context, in string) (string, error) {
		var decision string
		err := compose.ProcessState[*loopState](ctx, func(_ context.Context, s *loopState) error {
			s.steps++
			s.appendLog("reviewer")
			if s.steps > s.acceptAfter {
				// acceptSentinel = "accept" signal.
				// Must be non-empty: the Eino Pregel engine
				// drops empty-string outputs and the graph
				// terminates with no result.
				decision = acceptSentinel
			} else {
				// Non-empty = revise notes.
				decision = fmt.Sprintf("revise-%d", s.steps)
			}
			return nil
		})
		if err != nil {
			return "", err
		}
		return decision, nil
	})
	if err := g.AddLambdaNode("reviewer", reviewer, compose.WithNodeName("reviewer")); err != nil {
		return nil, fmt.Errorf("add reviewer: %w", err)
	}

	// ---- Node: reviser (back-edge to reviewer) ----
	reviser := compose.InvokableLambda(func(ctx context.Context, in string) (string, error) {
		_ = compose.ProcessState[*loopState](ctx, func(_ context.Context, s *loopState) error {
			s.appendLog("reviser")
			return nil
		})
		return "revised:" + in, nil
	})
	if err := g.AddLambdaNode("reviser", reviser, compose.WithNodeName("reviser")); err != nil {
		return nil, fmt.Errorf("add reviser: %w", err)
	}

	// ---- Branch on human → either "researcher" (accept) or
	// "planner" (revise, back-edge to retry the plan) ----
	humanBranch := compose.NewGraphBranch(
		func(ctx context.Context, _ string) (string, error) {
			var accept bool
			_ = compose.ProcessState[*loopState](ctx, func(_ context.Context, s *loopState) error {
				accept = s.humanShouldAccept
				return nil
			})
			if accept {
				return "researcher", nil
			}
			return "planner", nil
		},
		map[string]bool{"researcher": true, "planner": true},
	)
	if err := g.AddBranch("human", humanBranch); err != nil {
		return nil, fmt.Errorf("add human branch: %w", err)
	}

	// ---- Branch on reviewer: acceptSentinel→END, anything else→reviser ----
	reviewerBranch := compose.NewGraphBranch(
		func(_ context.Context, in string) (string, error) {
			if in == acceptSentinel {
				return compose.END, nil
			}
			return "reviser", nil
		},
		map[string]bool{compose.END: true, "reviser": true},
	)
	if err := g.AddBranch("reviewer", reviewerBranch); err != nil {
		return nil, fmt.Errorf("add reviewer branch: %w", err)
	}

	// ---- Edges ----
	if err := g.AddEdge(compose.START, "planner"); err != nil {
		return nil, fmt.Errorf("edge START->planner: %w", err)
	}
	if err := g.AddEdge("planner", "human"); err != nil {
		return nil, fmt.Errorf("edge planner->human: %w", err)
	}
	// human → researcher is one branch arm; the other arm
	// ("planner") is wired by the branch.
	if err := g.AddEdge("human", "researcher"); err != nil {
		return nil, fmt.Errorf("edge human->researcher: %w", err)
	}
	if err := g.AddEdge("researcher", "reviewer"); err != nil {
		return nil, fmt.Errorf("edge researcher->reviewer: %w", err)
	}
	// reviser → reviewer: the back-edge that creates the cycle.
	if err := g.AddEdge("reviser", "reviewer"); err != nil {
		return nil, fmt.Errorf("edge reviser->reviewer: %w", err)
	}
	// reviewer → END is wired by the branch's accept arm.

	// WithMaxRunSteps guards against infinite review cycles.
	// The graph has 5 nodes + 1 START, so we need enough headroom
	// for the test cases but still cap runaway loops. 30 covers
	// the cycle path (8 steps) + several retries; > 30 trips
	// the guard.
	runnable, err := g.Compile(ctx, compose.WithMaxRunSteps(30))
	if err != nil {
		return nil, fmt.Errorf("compile: %w", err)
	}

	out, err := runnable.Invoke(ctx, "query")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, fmt.Errorf("empty output")
	}

	// Read the log back via a fresh Invoke. We can't access the
	// per-run state from outside the graph (it's local to the
	// run), so we capture logs through a different channel: a
	// package-level pointer set by runSmoke, read after Invoke
	// returns. But since we ran the actual graph, the log lives
	// only in that per-run state. The simplest path is to
	// re-run with logging forwarded to a channel. For now we
	// return an empty log here; the real per-run log is
	// captured by the production code via the progress
	// callback.
	//
	// In this smoke test we don't actually need the log here
	// for assertions: the test cases instead verify the END
	// output. To verify the *path*, we add separate
	// "log capture" mode below.
	_ = out
	if currentLogState == nil {
		return nil, fmt.Errorf("currentLogState never set (GenLocalState not called?)")
	}
	return currentLogState.snapshot(), nil
}

// currentLogState holds the loopState created by the most recent
// GenLocalState invocation. The smoke tests are single-threaded
// and synchronous, so a plain package-level variable is enough.
var currentLogState *loopState

// count returns how many times s appears in xs.
func count(xs []string, s string) int {
	n := 0
	for _, x := range xs {
		if x == s {
			n++
		}
	}
	return n
}

// countPrefix returns how many xs strings start with prefix.
func countPrefix(xs []string, prefix string) int {
	n := 0
	for _, x := range xs {
		if len(x) >= len(prefix) && x[:len(prefix)] == prefix {
			n++
		}
	}
	return n
}

// assertLog verifies the execution path matches the expected
// sequence. An empty expected means "the test only cares that
// Invoke returned without error".
func assertLog(t *testing.T, got, want []string) {
	t.Helper()
	if len(want) == 0 {
		return
	}
	if len(got) != len(want) {
		t.Fatalf("log length: got %d want %d\n  got:  %v\n  want: %v",
			len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("log[%d]: got %q want %q\n  got:  %v\n  want: %v",
				i, got[i], want[i], got, want)
		}
	}
}

// assertLogPrefix verifies the execution path matches the
// expected sequence, comparing each got string's prefix to the
// corresponding want. Allows log lines to carry invocation
// suffixes (e.g. "human#1(in=...)") for debuggability while
// still asserting the underlying node sequence.
func assertLogPrefix(t *testing.T, got, want []string) {
	t.Helper()
	if len(want) == 0 {
		return
	}
	if len(got) != len(want) {
		t.Fatalf("log length: got %d want %d\n  got:  %v\n  want: %v",
			len(got), len(want), got, want)
	}
	for i := range want {
		if len(got[i]) < len(want[i]) || got[i][:len(want[i])] != want[i] {
			t.Fatalf("log[%d]: got %q want prefix %q\n  got:  %v\n  want: %v",
				i, got[i], want[i], got, want)
		}
	}
}
