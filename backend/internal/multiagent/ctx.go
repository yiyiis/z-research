// Package multiagent — ctx.go provides the context key +
// helpers used to inject per-run initial state into the
// graph.
//
// The graph's GenLocalState is fixed at compile time and
// creates a zero-value *ResearchState per run. To inject
// per-run values (query, HITL callback, etc.) we instead
// stash an initial *ResearchState in the context and read
// it from the planner node's pre-handler (see graph.go).
package multiagent

import "context"

type initialStateKey struct{}

// WithInitialState returns a child context carrying the
// given initial ResearchState. The planner node's
// pre-handler reads it via InitialStateFromContext.
func WithInitialState(ctx context.Context, s *ResearchState) context.Context {
	return context.WithValue(ctx, initialStateKey{}, s)
}

// InitialStateFromContext returns the *ResearchState stashed
// by WithInitialState, or nil if none was set.
func InitialStateFromContext(ctx context.Context) *ResearchState {
	if s, ok := ctx.Value(initialStateKey{}).(*ResearchState); ok {
		return s
	}
	return nil
}

type progressFnKey struct{}

// WithProgressFn stores a progress callback (stage, message)
// in the context. The graph nodes can retrieve it with
// ProgressFnFromContext to forward progress to the WebSocket
// without depending on the per-run state directly.
func WithProgressFn(ctx context.Context, fn func(stage, message string)) context.Context {
	return context.WithValue(ctx, progressFnKey{}, fn)
}

// ProgressFnFromContext returns the progress callback stored
// by WithProgressFn, or nil if none was set.
func ProgressFnFromContext(ctx context.Context) func(stage, message string) {
	if fn, ok := ctx.Value(progressFnKey{}).(func(string, string)); ok {
		return fn
	}
	return nil
}

// OnProgressFromContext is a thin alias for nodes that
// prefer the shorter name.
func OnProgressFromContext(ctx context.Context) func(stage, message string) {
	return ProgressFnFromContext(ctx)
}
