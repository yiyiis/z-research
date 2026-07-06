package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"z-research/backend/internal/researcher"
	"z-research/backend/internal/store"
)

// Server 持有所有依赖，注册路由。
//
// 双引擎：single（单 Agent）+ multi（多智能体），按请求 opts.Mode 路由。
// multi 可能为 nil（构造失败），前端选 multi 时报 503。
type Server struct {
	singleEngine researcher.EngineIface
	multiEngine  researcher.EngineIface // 可为 nil
	store        store.Store
}

// NewServer 创建 HTTP 服务。
func NewServer(single, multi researcher.EngineIface, st store.Store) *Server {
	return &Server{singleEngine: single, multiEngine: multi, store: st}
}

// pickEngine 按 Mode 选引擎：multi 路由到 multiEngine，单 engine 路由到 singleEngine。
func (s *Server) pickEngine(mode string) (researcher.EngineIface, bool) {
	switch mode {
	case "multi":
		if s.multiEngine == nil {
			return nil, false
		}
		return s.multiEngine, true
	case "single", "":
		return s.singleEngine, true
	default:
		return s.singleEngine, true
	}
}

// extractTitle 从 Markdown 报告中提取标题（首个 # 行，去掉 # 前缀），失败则截断查询。
func extractTitle(markdown, query string) string {
	for _, line := range strings.Split(markdown, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	if len([]rune(query)) > 40 {
		return string([]rune(query)[:40]) + "..."
	}
	return query
}

// toDTO 把 researcher.Source 转为 SourceDTO。
func toDTO(s researcher.Source) SourceDTO {
	return SourceDTO{N: s.N, URL: s.URL, Title: s.Title}
}

// toStoreSource 把 researcher.Source 转为 store.Source。
func toStoreSource(s researcher.Source) store.Source {
	return store.Source{N: s.N, URL: s.URL, Title: s.Title}
}

// wsUpgrader 把 HTTP 连接升级为 WebSocket。
// CheckOrigin 放开，允许开发期前端跨域。
var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// wsMessage 是通过 WebSocket 发送给前端的统一消息格式。
//
// 对齐 gpt-researcher 的设计：一条 JSON 帧，用 type 区分语义。
// 前端按 type 分发渲染。
//
// Type 取值：
//   - "progress"          引擎阶段进度（planner/human/researcher/writer 等）
//   - "sources"           报告的来源列表
//   - "report_chunk"      报告分片（流式）
//   - "done"              研究完成（含最终报告 + 报告 ID）
//   - "error"             错误
//   - "human_feedback"    多智能体模式专属：要求用户对大纲给反馈
type wsMessage struct {
	Type         string      `json:"type"`                    // 帧语义
	Stage        string      `json:"stage,omitempty"`         // progress 专用
	Message      string      `json:"message,omitempty"`       // 人类可读说明
	SectionTitle string      `json:"section_title,omitempty"` // progress 专用：当前章节（详细报告）
	Sources      []SourceDTO `json:"sources,omitempty"`       // sources/done 用
	Report       string      `json:"report,omitempty"`        // report_chunk/done 用：报告片段或全文
	ReportID     int64       `json:"report_id,omitempty"`     // done 用：持久化后的报告 ID

	// human_feedback 帧专属。
	Title    string   `json:"title,omitempty"`    // 大纲标题
	Sections []string `json:"sections,omitempty"` // 大纲分节
	Revision int      `json:"revision,omitempty"` // 第几次要求审核（0 = 首次）
}

// handleResearch 处理 WebSocket 升级后的研究通信。
//
// 协议（对齐 gpt-researcher 的 /ws 思路）：
//  1. 客户端连接后发一条 JSON：{"query":"研究问题", "mode":"multi"}
//  2. 服务端跑研究引擎，实时推送 progress 帧
//  3. （多智能体专属）引擎在 human_review 节点触发 human_feedback 帧，
//     客户端必须用 human_feedback_response 帧回复
//  4. 完成后推 sources + done（含报告全文与持久化 ID）
//  5. 失败推 error；任意一方可关闭连接
//
// WebSocket 是全双工长连接，没有 HTTP 的 idle 超时问题。
// 这里用了一个"读消息分发器"goroutine：首条消息是 query，
// 之后所有消息都路由到 feedbackCh（供 human_feedback 帧的
// 回调使用）。连接出错/关闭 → cancel() 中断引擎。
func (s *Server) handleResearch(c *gin.Context) {
	conn, err := wsUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[WS] 升级失败: %v\n", err)
		return
	}
	defer conn.Close()

	// 1. 读取客户端发来的研究请求。
	_, msgBytes, err := conn.ReadMessage()
	if err != nil {
		return
	}
	var req ResearchRequest
	if err := json.Unmarshal(msgBytes, &req); err != nil || strings.TrimSpace(req.Query) == "" {
		writeWS(conn, wsMessage{Type: "error", Message: "缺少必需字段 query"})
		return
	}

	// 用独立 context 跑引擎；连接关闭时取消。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// ---- WebSocket 读消息分发器 ----
	//
	// 首条消息（上面刚读过的 req）来自客户端。后续消息
	// 只有一种语义：human_feedback_response。所以这个
	// dispatcher 把后续消息路由到 feedbackCh，让
	// HumanFeedbackFn 阻塞读取。
	feedbackCh := make(chan *HumanFeedbackResponse, 1)
	go func() {
		defer close(feedbackCh)
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				cancel() // 客户端断开 → 取消引擎
				return
			}
			// Try to parse as human_feedback_response.
			// Unknown messages are silently dropped (the
			// client may also ping/pong; ignore those).
			var resp HumanFeedbackResponse
			if err := json.Unmarshal(msg, &resp); err != nil {
				continue
			}
			if resp.Type != "human_feedback_response" {
				continue
			}
			// Non-blocking send: if the channel is
			// already full, the previous response is
			// still being processed; drop the new one
			// (caller is blocked on the channel read).
			select {
			case feedbackCh <- &resp:
			default:
			}
		}
	}()

	// 并发写锁：progress 回调与最终 done 都要写 conn。
	var writeMu sync.Mutex
	safeWrite := func(m wsMessage) {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.WriteJSON(m)
	}

	// progress 回调：把引擎进度转发为 WebSocket progress 帧。
	onProgress := func(p researcher.Progress) {
		if ctx.Err() != nil {
			return
		}
		safeWrite(wsMessage{
			Type:         "progress",
			Stage:        string(p.Stage),
			Message:      p.Message,
			SectionTitle: p.SectionTitle,
		})
	}

	// 流式报告回调：把每个生成块实时推给前端。
	onReportChunk := func(chunk, accu string) {
		if ctx.Err() != nil {
			return
		}
		safeWrite(wsMessage{Type: "report_chunk", Report: accu})
	}

	// 构造 researcher.Options。req.Mode 决定走单 Agent 还是
	// 多智能体引擎（EngineRouter 根据 opts.Mode 路由）。
	opts := &researcher.Options{
		TaskID:     req.TaskID,
		ReportType: researcher.ReportType(req.ReportType), // brief/detailed（仅单 Agent 生效）
	}
	if req.Mode != "" {
		m := req.Mode
		opts.Mode = &m
	}
	if req.Mode == "multi" {
		opts.HumanFeedbackFn = makeHumanFeedbackFn(ctx, safeWrite, feedbackCh)
		// req.HitL 是前端勾选框的状态。装到
		// opts.EnableHITL，Engine.Run 会把它传到
		// initial state，从而 human_review 节点能
		// 决定是否真正阻塞（否则即使 fn 装了，
		// 节点仍会因 enableHITL=false 自动 accept）。
		hitlOn := req.HitL
		opts.EnableHITL = &hitlOn
	} else if req.Mode == "single" {
		// Explicit single mode: no feedback fn.
		opts.HumanFeedbackFn = nil
		off := false
		opts.EnableHITL = &off
	}
	// If mode is empty, opts.Mode is nil → engine fallbacks to default (cfg.Mode).

	// 按 req.Mode 选引擎（multi 可能为 nil）。
	engine, ok := s.pickEngine(req.Mode)
	if !ok {
		safeWrite(wsMessage{Type: "error", Message: "multi 模式不可用（构造失败或未启动），请改用 single"})
		return
	}

	// 跑研究引擎（用 defer recover 防止 panic 直接断连）。
	var report *researcher.FinalReport
	func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "[PANIC] handleResearch: %v\n", r)
				safeWrite(wsMessage{Type: "error", Message: fmt.Sprintf("内部错误: %v", r)})
				cancel()
			}
		}()
		report, err = engine.Run(ctx, req.Query, opts, onProgress, onReportChunk)
	}()
	if err != nil {
		if ctx.Err() != nil {
			return // 客户端断开
		}
		safeWrite(wsMessage{Type: "error", Message: err.Error()})
		return
	}

	// 推送来源列表。
	srcDTOs := make([]SourceDTO, len(report.Sources))
	for i, src := range report.Sources {
		srcDTOs[i] = toDTO(src)
	}
	safeWrite(wsMessage{Type: "sources", Sources: srcDTOs})

	// 持久化报告。
	title := extractTitle(report.Markdown, req.Query)
	storeSources := make([]store.Source, len(report.Sources))
	for i, src := range report.Sources {
		storeSources[i] = toStoreSource(src)
	}
	var reportID int64
	if saved, saveErr := s.store.Save(ctx, req.Query, title, report.Markdown, storeSources); saveErr == nil {
		reportID = saved.ID
	}

	safeWrite(wsMessage{Type: "done", Report: report.Markdown, Sources: srcDTOs, ReportID: reportID})
}

// makeHumanFeedbackFn builds the callback the multi-agent
// engine calls from its human_review node. The callback
// sends a human_feedback frame over the WebSocket and then
// blocks until the client posts a human_feedback_response,
// or the context is cancelled (client disconnect).
//
// Concurrency: the WebSocket dispatcher goroutine writes
// to feedbackCh. The engine Run() is a single goroutine
// for a given WS connection. So the channel is 1-buffered
// and the callback is the sole reader.
func makeHumanFeedbackFn(
	ctx context.Context,
	safeWrite func(wsMessage),
	feedbackCh <-chan *HumanFeedbackResponse,
) researcher.HumanFeedbackFn {
	return func(ctx context.Context, plan researcher.HumanReviewPlan) (string, error) {
		// Send the human_feedback frame to the client.
		safeWrite(wsMessage{
			Type:     "human_feedback",
			Title:    plan.Title,
			Sections: plan.Sections,
			Revision: plan.Revision,
		})
		// Block on the client's response or ctx cancel.
		select {
		case resp, ok := <-feedbackCh:
			if !ok {
				return "", ctx.Err()
			}
			if resp.Accept {
				return "", nil
			}
			return resp.Notes, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// writeWS 写一条 WebSocket 文本帧，出错静默忽略。
func writeWS(conn *websocket.Conn, m wsMessage) {
	_ = conn.WriteJSON(m)
}

// handleListReports 处理 GET /api/reports，返回历史列表（不含正文）。
func (s *Server) handleListReports(c *gin.Context) {
	limit := 50
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	reports, err := s.store.List(c.Request.Context(), limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	items := make([]ReportListItem, 0, len(reports))
	for _, r := range reports {
		srcs := make([]SourceDTO, len(r.Sources))
		for i, src := range r.Sources {
			srcs[i] = SourceDTO{N: src.N, URL: src.URL, Title: src.Title}
		}
		items = append(items, ReportListItem{
			ID:        r.ID,
			Query:     r.Query,
			Title:     r.Title,
			Sources:   srcs,
			CreatedAt: r.CreatedAt.Format("2006-01-02 15:04:05"),
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// handleGetReport 处理 GET /api/reports/:id，返回单篇报告全文。
func (s *Server) handleGetReport(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 id"})
		return
	}
	r, err := s.store.Get(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	srcs := make([]SourceDTO, len(r.Sources))
	for i, src := range r.Sources {
		srcs[i] = SourceDTO{N: src.N, URL: src.URL, Title: src.Title}
	}
	c.JSON(http.StatusOK, ReportDetail{
		ID:        r.ID,
		Query:     r.Query,
		Title:     r.Title,
		Content:   r.Content,
		Sources:   srcs,
		CreatedAt: r.CreatedAt.Format("2006-01-02 15:04:05"),
	})
}

// handleDeleteReport 处理 DELETE /api/reports/:id。
func (s *Server) handleDeleteReport(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 id"})
		return
	}
	if err := s.store.Delete(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
