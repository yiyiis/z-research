package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"z-research/backend/internal/researcher"
	"z-research/backend/internal/store"
)

// fakeEngine 是 researcher.EngineIface 的测试替身，不做真实研究，
// 只回放进度事件，并返回固定报告。
type fakeEngine struct {
	progresses []researcher.Progress
	report     *researcher.FinalReport
	err        error
}

func (f *fakeEngine) Run(ctx context.Context, query string, opts *researcher.Options, onProgress researcher.EventFn, onReportChunk researcher.ReportChunkFn) (*researcher.FinalReport, error) {
	for _, p := range f.progresses {
		if onProgress != nil {
			onProgress(p)
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	// 模拟流式报告：把报告正文按块回调（与真实引擎行为一致）。
	if onReportChunk != nil && f.report != nil {
		onReportChunk(f.report.Markdown, f.report.Markdown)
	}
	return f.report, nil
}

// newTestServer 用假引擎 + 临时 SQLite store 构造测试服务，并返回一个指向 :8080 的 wsURL。
func newTestServer(t *testing.T, eng researcher.EngineIface) (*Server, store.Store, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := store.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := NewServer(eng, st)
	return srv, st, srv.Router(false)
}

// dialWS 启动一个 httptest server 并建立 WebSocket 连接。
func dialWS(t *testing.T, r *gin.Engine) (*websocket.Conn, func()) {
	t.Helper()
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	return conn, func() { _ = conn.Close() }
}

// readMsg 读一条 WS 消息，超时 5s。
func readMsg(t *testing.T, conn *websocket.Conn) wsMessage {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	var m wsMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Unmarshal %q: %v", string(data), err)
	}
	return m
}

// TestWS_Research_FullFlow 验证 WebSocket 研究全流程：
// 连接 → 发 query → 收 progress* → 收 sources → 收 done（含报告）。
func TestWS_Research_FullFlow(t *testing.T) {
	eng := &fakeEngine{
		progresses: []researcher.Progress{
			{Stage: researcher.StageRole, Message: "角色: 测试员"},
			{Stage: researcher.StagePlanning, Message: "规划完成"},
		},
		report: &researcher.FinalReport{
			Markdown: "# 测试报告\n正文内容",
			Sources:  []researcher.Source{{N: 1, URL: "http://a.com", Title: "A"}},
		},
	}
	_, _, r := newTestServer(t, eng)
	conn, cleanup := dialWS(t, r)
	defer cleanup()

	// 发送研究请求。
	if err := conn.WriteJSON(ResearchRequest{Query: "测试查询"}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}

	// 收集消息，直到收到 done。
	var got []wsMessage
	var done *wsMessage
	for {
		m := readMsg(t, conn)
		got = append(got, m)
		if m.Type == "done" {
			done = &got[len(got)-1]
			break
		}
		if m.Type == "error" {
			t.Fatalf("收到 error: %s", m.Message)
		}
	}

	// 验证至少有 progress 帧。
	progressCount := 0
	for _, m := range got {
		if m.Type == "progress" {
			progressCount++
		}
	}
	if progressCount < 2 {
		t.Errorf("应至少收到 2 个 progress，收到 %d", progressCount)
	}
	// 验证 done 含报告与来源。
	if done == nil || done.Report != "# 测试报告\n正文内容" {
		t.Errorf("done 报告不匹配: %+v", done)
	}
	if len(done.Sources) != 1 || done.Sources[0].URL != "http://a.com" {
		t.Errorf("done 来源不匹配: %+v", done.Sources)
	}
	if done.ReportID == 0 {
		t.Error("done 应有持久化的 report_id")
	}
	t.Logf("✅ WS 全流程通过：收到 %d 条消息，report_id=%d", len(got), done.ReportID)
}

// TestWS_MissingQuery 发空 query 应收到 error 帧。
func TestWS_MissingQuery(t *testing.T) {
	eng := &fakeEngine{report: &researcher.FinalReport{Markdown: "# x"}}
	_, _, r := newTestServer(t, eng)
	conn, cleanup := dialWS(t, r)
	defer cleanup()

	if err := conn.WriteJSON(ResearchRequest{Query: ""}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	m := readMsg(t, conn)
	if m.Type != "error" {
		t.Errorf("空 query 应收到 error，收到 %s", m.Type)
	}
}

// TestWS_EngineError 引擎报错应推送 error 帧。
func TestWS_EngineError(t *testing.T) {
	eng := &fakeEngine{err: errFake}
	_, _, r := newTestServer(t, eng)
	conn, cleanup := dialWS(t, r)
	defer cleanup()

	_ = conn.WriteJSON(ResearchRequest{Query: "q"})
	m := readMsg(t, conn)
	if m.Type != "error" {
		t.Errorf("引擎出错应推 error，收到 %s", m.Type)
	}
}

// TestREST_ReportCRUD 验证报告 CRUD（独立于 WS）。
func TestREST_ReportCRUD(t *testing.T) {
	eng := &fakeEngine{
		report: &researcher.FinalReport{
			Markdown: "# CRUD 测试\n正文",
			Sources:  []researcher.Source{{N: 1, URL: "http://b.com", Title: "B"}},
		},
	}
	_, st, r := newTestServer(t, eng)

	// 直接存一篇报告（模拟已完成的报告），拿到 id。
	saved, err := st.Save(context.Background(), "CRUD 查询", "CRUD 测试", "# CRUD 测试\n正文",
		[]store.Source{{N: 1, URL: "http://b.com", Title: "B"}})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	id := saved.ID

	// 列表应有 1 条。
	w := doReq(r, "GET", "/api/reports", nil)
	var listResp struct {
		Items []ReportListItem `json:"items"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &listResp)
	if len(listResp.Items) != 1 || listResp.Items[0].ID != id {
		t.Errorf("列表应含 1 条 id=%d, 得到 %+v", id, listResp.Items)
	}
	if listResp.Items[0].Title != "CRUD 测试" {
		t.Errorf("Title 应为 CRUD 测试, 得到 %q", listResp.Items[0].Title)
	}

	// 详情含正文。
	w = doReq(r, "GET", "/api/reports/"+itoa(id), nil)
	var detail ReportDetail
	_ = json.Unmarshal(w.Body.Bytes(), &detail)
	if detail.Content != "# CRUD 测试\n正文" {
		t.Errorf("详情正文不匹配: %q", detail.Content)
	}

	// 删除后 404。
	w = doReq(r, "DELETE", "/api/reports/"+itoa(id), nil)
	if w.Code != 200 {
		t.Errorf("delete 应 200, 得到 %d", w.Code)
	}
	w = doReq(r, "GET", "/api/reports/"+itoa(id), nil)
	if w.Code != 404 {
		t.Errorf("删除后 get 应 404, 得到 %d", w.Code)
	}
}

// errFake 是测试用错误。
var errFake = errFakeType{}

type errFakeType struct{}

func (errFakeType) Error() string { return "fake engine error" }
