// Package researcher — router.go defines EngineRouter, an
// EngineIface implementation that holds BOTH a single-agent
// engine and a multi-agent engine, and routes each Run()
// call to the right one based on opts.Mode.
//
// This fixes the bug where the engine was bound at startup
// based on cfg.Mode, so the frontend's "single/multi" toggle
// was silently ignored: a server started with
// ENGINE_MODE=multi would always run the multi-agent Browser
// node even when the user picked "单 Agent" in the UI.
package researcher

import (
	"context"
	"fmt"
)

// EngineRouter routes Run() calls to either the single-agent
// or multi-agent engine based on opts.Mode. It implements
// EngineIface so the api layer is unchanged.
//
//   - opts.Mode == "single" → single engine
//   - opts.Mode == "multi"  → multi engine
//   - opts.Mode == nil/""   → defaultEngine (configured at
//     construction time, typically cfg.Mode)
type EngineRouter struct {
	single        EngineIface // may be nil if not built
	multi         EngineIface // may be nil if not built
	defaultEngine EngineIface // fallback when opts.Mode is empty
}

// NewEngineRouter constructs a router. At least one of
// single/multi must be non-nil. defaultEngine is used when
// opts.Mode is empty/nil; pass the cfg.Mode-derived engine.
func NewEngineRouter(single, multi, defaultEngine EngineIface) (*EngineRouter, error) {
	if defaultEngine == nil {
		return nil, fmt.Errorf("defaultEngine must not be nil")
	}
	return &EngineRouter{
		single:        single,
		multi:         multi,
		defaultEngine: defaultEngine,
	}, nil
}

// Compile-time assertion.
var _ EngineIface = (*EngineRouter)(nil)

// Run routes to the appropriate engine based on opts.Mode.
func (r *EngineRouter) Run(
	ctx context.Context,
	query string,
	opts *Options,
	onProgress EventFn,
	onReportChunk ReportChunkFn,
) (*FinalReport, error) {
	engine := r.pick(opts)
	if engine == nil {
		return nil, fmt.Errorf("no engine available for mode %v", modeStr(opts))
	}
	return engine.Run(ctx, query, opts, onProgress, onReportChunk)
}

// pick returns the engine for the given opts.Mode.
func (r *EngineRouter) pick(opts *Options) EngineIface {
	if opts == nil || opts.Mode == nil || *opts.Mode == "" {
		return r.defaultEngine
	}
	switch *opts.Mode {
	case "single":
		return r.single
	case "multi":
		return r.multi
	default:
		return r.defaultEngine
	}
}

func modeStr(opts *Options) string {
	if opts == nil || opts.Mode == nil {
		return ""
	}
	return *opts.Mode
}
