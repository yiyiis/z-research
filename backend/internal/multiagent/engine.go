// Package multiagent — engine.go exposes the multi-agent
// pipeline as a researcher.EngineIface implementation. This
// lets the existing api/handlers.go WebSocket layer treat
// the multi-agent flow as a drop-in replacement for the
// single-agent engine. main.go selects which engine to wire
// up based on cfg.Mode (or per-request Options.Mode).
package multiagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/cloudwego/eino/compose"

	"z-research/backend/internal/config"
	"z-research/backend/internal/llm"
	"z-research/backend/internal/researcher"
	"z-research/backend/internal/store"
)

// Engine is the multi-agent implementation of
// researcher.EngineIface. It wraps the pre-compiled outer
// Eino graph and adapts it to the Run(ctx, query, opts, onProgress,
// onReportChunk) signature.
type Engine struct {
	cfg   *config.Config
	llm   *llm.LLM
	inner *researcher.Researcher
	cp    compose.CheckPointStore

	// runnable is the compiled outer graph. Compiled
	// once at construction so each Run() is just an
	// Invoke. Compilation cost is amortized.
	runnable compose.Runnable[string, string]
}

// Compile-time assertion: *Engine implements EngineIface.
var _ researcher.EngineIface = (*Engine)(nil)

// NewEngine builds and compiles the multi-agent engine.
// Compilation is non-trivial (compiles the inner draft
// graph too) so this should be called once at server
// startup, not per-request.
//
// st is the project Store. If it exposes a *sql.DB (e.g.
// the SQLiteStore), a SQLiteCheckPointStore is created
// and wired into the graph via compose.WithCheckPointStore.
// This enables per-run resume via Options.TaskID + a
// follow-up Invoke with compose.WithCheckPointID.
//
// If st is nil or does not expose a *sql.DB, the graph
// runs without checkpointing (degraded mode).
func NewEngine(ctx context.Context, cfg *config.Config, llmClient *llm.LLM, inner *researcher.Researcher, st store.Store) (*Engine, error) {
	g := BuildOuterGraph(ctx, cfg, llmClient, inner)

	compileOpts := []compose.GraphCompileOption{
		compose.WithMaxRunSteps(cfg.MaxRunSteps),
	}
	var cp compose.CheckPointStore
	if st != nil {
		var err error
		cp, err = NewSQLiteCheckPointStore(ctx, st)
		if err == nil {
			compileOpts = append(compileOpts, compose.WithCheckPointStore(cp))
		} else {
			// Non-fatal: log and continue without
			// checkpointing.
			fmt.Fprintf(os.Stderr, "multiagent: checkpoint store unavailable, running without resume: %v\n", err)
		}
	}

	runnable, err := g.Compile(ctx, compileOpts...)
	if err != nil {
		return nil, fmt.Errorf("compile outer graph: %w", err)
	}
	return &Engine{cfg: cfg, llm: llmClient, inner: inner, cp: cp, runnable: runnable}, nil
}

// Run executes the multi-agent pipeline. Signature matches
// researcher.Engine.Run so api/handlers.go can call it
// interchangeably with the single-agent engine.
//
// Per-run overrides come from opts (researcher.Options). The
// relevant fields for multi-agent are:
//   - Mode            — ignored here; caller routes
//     single vs multi at construction time.
//   - HumanFeedbackFn — wired into the human_review node.
//   - TaskID          — used for checkpoint resume (see
//     checkpoint.go, future work).
//
// The graph's I/O type is *ResearchState (set in
// graph.go via WithGenLocalState). The input state is
// populated from the request + opts before Invoke; the
// output state holds the final report and sources.
func (e *Engine) Run(
	ctx context.Context,
	query string,
	opts *researcher.Options,
	onProgress researcher.EventFn,
	onReportChunk researcher.ReportChunkFn,
) (*researcher.FinalReport, error) {
	// Resolve per-run config from opts. nil opts = use
	// the cfg defaults.
	feedbackFn := researcher.HumanFeedbackFn(nil)
	// Per-request EnableHITL (from frontend checkbox)
	// overrides cfg.EnableHITL. nil = use cfg.
	enableHITL := e.cfg.EnableHITL
	maxSecs := e.cfg.MaxSections
	maxPlans := e.cfg.MaxPlanRevisions
	taskID := ""

	if opts != nil {
		feedbackFn = opts.HumanFeedbackFn
		taskID = opts.TaskID
		if opts.EnableHITL != nil {
			enableHITL = *opts.EnableHITL
		}
	}
	if taskID == "" {
		taskID = randomTaskID()
	}

	// Build the per-run initial state. This state is
	// not the one the graph uses (Eino's
	// WithGenLocalState creates its own via the
	// closure in graph.go); we use it to drive the
	// graph's state initializer.
	//
	// The graph's GenLocalState is fixed at compile
	// time. To inject per-run values we instead use
	// the pre-built planner pre-handler: a
	// compose.WithStatePreHandler is attached to
	// the planner node (via buildOuterGraph) that
	// overwrites state fields from a context value
	// that Run sets here. See graph.go.
	//
	// For now we set the context value that the
	// pre-handler reads.
	initial := &ResearchState{
		Query:            query,
		MaxSections:      maxSecs,
		MaxPlanRevisions: maxPlans,
		EnableHITL:       enableHITL,
		HumanFeedbackFn:  feedbackFn,
		OnReportChunk:    onReportChunk,
	}
	if onProgress != nil {
		initial.OnProgress = func(stage, message string) {
			onProgress(researcher.Progress{Stage: researcher.Stage(stage), Message: message})
		}
		// Also expose the progress fn via context so
		// graph nodes (e.g. Browser) can forward
		// intermediate results (initial_research
		// summary) without going through the per-run
		// state.
		ctx = WithProgressFn(ctx, func(stage, message string) {
			onProgress(researcher.Progress{Stage: researcher.Stage(stage), Message: message})
		})
	}
	ctx = WithInitialState(ctx, initial)

	// Per-invoke options. If a task ID is available AND
	// the engine was built with a CheckPointStore, pass
	// the ID so Eino either resumes from the saved
	// checkpoint (if present) or starts a new one (if
	// not). See compose.WithCheckPointID.
	invokeOpts := []compose.Option{}
	if e.cp != nil && taskID != "" {
		invokeOpts = append(invokeOpts, compose.WithCheckPointID(taskID))
	}

	out, err := e.runnable.Invoke(ctx, query, invokeOpts...)
	if err != nil {
		return nil, err
	}

	return &researcher.FinalReport{
		Markdown: out,
		Sources:  nil, // see state-on-output TODO below
	}, nil
}

// randomTaskID returns a 16-char hex string suitable as a
// checkpoint key. It is exported for tests via the public
// API but unexported within the package.
func randomTaskID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
