package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"z-research/backend/internal/researcher"
)

// hitlEngine is a fakeEngine variant that exercises the
// HumanFeedbackFn callback. When Run() is called, it:
//
//  1. Records opts.HumanFeedbackFn and the engine opts it
//     was given.
//  2. Calls the HumanFeedbackFn with a fixed plan, blocking
//     until the callback returns (or ctx is cancelled).
//  3. If the callback returns an empty string ("accept"),
//     continues and returns the canned report. If non-empty,
//     records the feedback and returns the canned report
//     anyway (real engine would loop back to planner).
type hitlEngine struct {
	feedbackFn   researcher.HumanFeedbackFn
	enableHITL   *bool // captured from opts.EnableHITL
	planPassed   researcher.HumanReviewPlan
	feedbackRecv string
	err          error
	report       *researcher.FinalReport
}

func (h *hitlEngine) Run(ctx context.Context, query string, opts *researcher.Options, onProgress researcher.EventFn, onReportChunk researcher.ReportChunkFn) (*researcher.FinalReport, error) {
	h.feedbackFn = opts.HumanFeedbackFn
	h.enableHITL = opts.EnableHITL
	if onProgress != nil {
		onProgress(researcher.Progress{Stage: "planner", Message: "outline ready"})
	}
	// Mirror the production check: only block on
	// feedbackFn when EnableHITL is set.
	hitlOn := h.enableHITL != nil && *h.enableHITL
	if h.feedbackFn == nil || !hitlOn {
		// No feedback fn OR HITL disabled → auto-accept path.
		return h.report, h.err
	}
	plan := researcher.HumanReviewPlan{
		Title:    "Test Report",
		Sections: []string{"Section A", "Section B"},
		Revision: 0,
	}
	h.planPassed = plan
	fb, err := h.feedbackFn(ctx, plan)
	if err != nil {
		return nil, err
	}
	h.feedbackRecv = fb
	if onProgress != nil {
		onProgress(researcher.Progress{Stage: "writer", Message: "writing"})
	}
	if onReportChunk != nil && h.report != nil {
		onReportChunk(h.report.Markdown, h.report.Markdown)
	}
	return h.report, h.err
}

// TestWS_HITL_FullFlow 验证多智能体模式的 HITL WebSocket
// 流程：
//  1. 客户端发 mode=multi 的 query；
//  2. 服务端引擎触发 HumanFeedbackFn；
//  3. 服务端向客户端发 human_feedback 帧（含 title+sections）；
//  4. 客户端发 human_feedback_response（accept=true）；
//  5. 服务端收到反馈后继续，发送最终 done 帧。
func TestWS_HITL_FullFlow(t *testing.T) {
	eng := &hitlEngine{
		report: &researcher.FinalReport{
			Markdown: "# Test Report\nBody",
			Sources:  []researcher.Source{{N: 1, URL: "http://a", Title: "A"}},
		},
	}
	_, _, r := newTestServer(t, eng)

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// 1. Send query in multi mode.
	if err := conn.WriteJSON(ResearchRequest{Query: "test query", Mode: "multi", HitL: true}); err != nil {
		t.Fatalf("write query: %v", err)
	}

	// 2. Read frames until we hit the human_feedback
	//    request. Skip progress/sources frames.
	var feedback wsMessage
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var m wsMessage
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if m.Type == "human_feedback" {
			feedback = m
			break
		}
		// Otherwise: progress / report_chunk / etc.
		// We just skip and keep reading.
	}

	// 3. Verify the human_feedback frame.
	if feedback.Type != "human_feedback" {
		t.Fatalf("expected human_feedback frame, got %+v", feedback)
	}
	if feedback.Title != "Test Report" {
		t.Errorf("expected title 'Test Report', got %q", feedback.Title)
	}
	if len(feedback.Sections) != 2 {
		t.Errorf("expected 2 sections, got %d: %v", len(feedback.Sections), feedback.Sections)
	}

	// 4. Reply with accept=true.
	if err := conn.WriteJSON(HumanFeedbackResponse{
		Type:   "human_feedback_response",
		Accept: true,
	}); err != nil {
		t.Fatalf("write feedback: %v", err)
	}

	// 5. Read remaining frames; expect done.
	deadline = time.Now().Add(5 * time.Second)
	var gotDone bool
	for !gotDone {
		_ = conn.SetReadDeadline(deadline)
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read done: %v", err)
		}
		var m wsMessage
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if m.Type == "done" {
			gotDone = true
			if m.Report != "# Test Report\nBody" {
				t.Errorf("done.report mismatch: %q", m.Report)
			}
		}
		if m.Type == "error" {
			t.Fatalf("unexpected error frame: %s", m.Message)
		}
	}

	// 6. Verify the engine saw the feedback.
	if eng.feedbackRecv != "" {
		t.Errorf("expected feedbackRecv to be empty (accept), got %q", eng.feedbackRecv)
	}
	if eng.planPassed.Title != "Test Report" {
		t.Errorf("plan title mismatch: %q", eng.planPassed.Title)
	}
}

// TestWS_HITL_Revise 验证 revise 路径：客户端发 accept=false + 反馈意见
// → 引擎收到非空 feedback 字符串。
func TestWS_HITL_Revise(t *testing.T) {
	eng := &hitlEngine{
		report: &researcher.FinalReport{Markdown: "# R"},
	}
	_, _, r := newTestServer(t, eng)

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(ResearchRequest{Query: "q", Mode: "multi", HitL: true}); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Read until human_feedback.
	deadline := time.Now().Add(5 * time.Second)
	for {
		_ = conn.SetReadDeadline(deadline)
		_, data, _ := conn.ReadMessage()
		var m wsMessage
		_ = json.Unmarshal(data, &m)
		if m.Type == "human_feedback" {
			break
		}
	}

	// Revise with a custom note.
	if err := conn.WriteJSON(HumanFeedbackResponse{
		Type:   "human_feedback_response",
		Accept: false,
		Notes:  "请增加一节'未来展望'",
	}); err != nil {
		t.Fatalf("write feedback: %v", err)
	}

	// Drain frames until done.
	deadline = time.Now().Add(5 * time.Second)
	for {
		_ = conn.SetReadDeadline(deadline)
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var m wsMessage
		_ = json.Unmarshal(data, &m)
		if m.Type == "done" {
			break
		}
		if m.Type == "error" {
			t.Fatalf("error: %s", m.Message)
		}
	}

	if eng.feedbackRecv != "请增加一节'未来展望'" {
		t.Errorf("expected feedback note to be received, got %q", eng.feedbackRecv)
	}
}

// TestWS_HITL_DisconnectDuringReview 验证：客户端在收到
// human_feedback 帧后立即断连 → 引擎的 HumanFeedbackFn
// 阻塞返回 ctx error → Run 返回 error → 服务端推 error
// 或客户端断连。Engine.Run 在测试中阻塞等反馈；取消 ctx
// 后应立刻返回。
func TestWS_HITL_DisconnectDuringReview(t *testing.T) {
	// Slow feedback engine: never returns until ctx
	// is cancelled. We close the conn from the client
	// side; the dispatcher goroutine will see the
	// read error, cancel the ctx, and the callback
	// will return ctx.Err().
	eng := &hitlEngine{
		report: &researcher.FinalReport{Markdown: "# X"},
	}
	_, _, r := newTestServer(t, eng)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	if err := conn.WriteJSON(ResearchRequest{Query: "q", Mode: "multi", HitL: true}); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Read until human_feedback arrives.
	deadline := time.Now().Add(5 * time.Second)
	for {
		_ = conn.SetReadDeadline(deadline)
		_, data, _ := conn.ReadMessage()
		var m wsMessage
		_ = json.Unmarshal(data, &m)
		if m.Type == "human_feedback" {
			break
		}
	}

	// Disconnect abruptly. The dispatcher will ReadMessage
	// error, cancel the ctx; the feedbackFn (still blocked
	// on select) will return ctx.Err(). The engine's Run
	// will return that error.
	_ = conn.Close()

	// The server-side engine may or may not have written
	// any further frames before the ctx propagated.
	// We don't assert anything here — the test passes if
	// the process doesn't hang or panic. The dispatcher
	// goroutine exits via the read error.
}

// TestWS_HITL_EnableHITLPropagated verifies the api layer
// forwards ResearchRequest.HitL into researcher.Options.
// EnableHITL. This is what tells the multi-agent
// human_review node whether to actually block or
// auto-accept. Regression test for the bug where
// frontend's HITL checkbox was silently ignored.
func TestWS_HITL_EnableHITLPropagated(t *testing.T) {
	cases := []struct {
		name           string
		req            ResearchRequest
		wantEnableHITL *bool // expected opts.EnableHITL captured
	}{
		{
			name:           "multi + hitl=true → EnableHITL=true",
			req:            ResearchRequest{Query: "q", Mode: "multi", HitL: true},
			wantEnableHITL: ptr(true),
		},
		{
			name:           "multi + hitl=false → EnableHITL=false",
			req:            ResearchRequest{Query: "q", Mode: "multi", HitL: false},
			wantEnableHITL: ptr(false),
		},
		{
			name:           "single + hitl=true → EnableHITL=false (single ignores)",
			req:            ResearchRequest{Query: "q", Mode: "single", HitL: true},
			wantEnableHITL: ptr(false),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng := &hitlEngine{report: &researcher.FinalReport{Markdown: "# x"}}
			_, _, r := newTestServer(t, eng)
			srv := httptest.NewServer(r)
			t.Cleanup(srv.Close)
			wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
			conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer conn.Close()
			if err := conn.WriteJSON(tc.req); err != nil {
				t.Fatalf("write: %v", err)
			}
			// Drain frames until done. Use a short
			// deadline; multi+hitl will block on
			// human_feedback (not invoked here since
			// hitlEngine doesn't have a connected
			// dispatcher). The important thing is
			// opts.EnableHITL was captured.
			deadline := time.Now().Add(3 * time.Second)
			for {
				_ = conn.SetReadDeadline(deadline)
				_, data, err := conn.ReadMessage()
				if err != nil {
					break
				}
				var m wsMessage
				_ = json.Unmarshal(data, &m)
				if m.Type == "done" || m.Type == "error" {
					break
				}
			}
			if eng.enableHITL == nil && tc.wantEnableHITL != nil {
				t.Fatalf("opts.EnableHITL was nil, want %v", *tc.wantEnableHITL)
			}
			if eng.enableHITL != nil && tc.wantEnableHITL == nil {
				t.Fatalf("opts.EnableHITL was %v, want nil", *eng.enableHITL)
			}
			if eng.enableHITL != nil && *eng.enableHITL != *tc.wantEnableHITL {
				t.Fatalf("opts.EnableHITL = %v, want %v", *eng.enableHITL, *tc.wantEnableHITL)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }
