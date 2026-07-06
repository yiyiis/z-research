package researcher

import (
	"context"
	"errors"
	"testing"
)

// recordingEngine 是一个记录自己被调用次数的假引擎。
type recordingEngine struct {
	name   string
	called int
}

func (e *recordingEngine) Run(_ context.Context, _ string, _ *Options, _ EventFn, _ ReportChunkFn) (*FinalReport, error) {
	e.called++
	return &FinalReport{Markdown: e.name}, nil
}

func strPtr(s string) *string { return &s }

// TestEngineRouter_RoutesByMode 验证 router 根据 opts.Mode
// 路由到正确的引擎。
func TestEngineRouter_RoutesByMode(t *testing.T) {
	single := &recordingEngine{name: "single"}
	multi := &recordingEngine{name: "multi"}

	r, err := NewEngineRouter(single, multi, single) // default=single
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		mode    *string
		wantRun string
	}{
		{"mode=single", strPtr("single"), "single"},
		{"mode=multi", strPtr("multi"), "multi"},
		{"mode=nil → default", nil, "single"},
		{"mode=空串 → default", strPtr(""), "single"},
		{"mode=未知值 → default", strPtr("bogus"), "single"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			single.called = 0
			multi.called = 0
			report, err := r.Run(context.Background(), "q", &Options{Mode: c.mode}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if report.Markdown != c.wantRun {
				t.Errorf("got %q, want %q", report.Markdown, c.wantRun)
			}
			// 也确认正确引擎被调用一次。
			switch c.wantRun {
			case "single":
				if single.called != 1 {
					t.Errorf("single called %d times, want 1", single.called)
				}
			case "multi":
				if multi.called != 1 {
					t.Errorf("multi called %d times, want 1", multi.called)
				}
			}
		})
	}
}

// TestEngineRouter_NilMultiEngine 验证 router 在 multi 引擎
// 不可用时（构造失败），选 multi 会返回清晰错误。
func TestEngineRouter_NilMultiEngine(t *testing.T) {
	single := &recordingEngine{name: "single"}
	r, err := NewEngineRouter(single, nil, single) // multi=nil
	if err != nil {
		t.Fatal(err)
	}

	// 选 multi → 应该报错。
	_, err = r.Run(context.Background(), "q", &Options{Mode: strPtr("multi")}, nil, nil)
	if err == nil {
		t.Fatal("expected error when multi engine is nil, got nil")
	}
	// 错误信息应该提到 "multi" 或 "no engine"。
	if !contains(err.Error(), "engine") {
		t.Errorf("expected error mentioning engine, got: %v", err)
	}

	// 选 single 仍正常。
	single.called = 0
	report, err := r.Run(context.Background(), "q", &Options{Mode: strPtr("single")}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.Markdown != "single" {
		t.Errorf("got %q, want single", report.Markdown)
	}
	if single.called != 1 {
		t.Errorf("single called %d, want 1", single.called)
	}
}

// TestEngineRouter_NilDefaultPanicsAtConstruction 验证
// 构造时 defaultEngine 不能为 nil。
func TestEngineRouter_NilDefaultRejected(t *testing.T) {
	_, err := NewEngineRouter(nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for nil defaultEngine, got nil")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

var _ = errors.New // keep import used
