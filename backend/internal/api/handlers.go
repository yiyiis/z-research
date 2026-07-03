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
type Server struct {
	engine researcher.EngineIface // 研究引擎（通过接口注入，便于测试）
	store  store.Store
}

// NewServer 创建 HTTP 服务。
func NewServer(engine researcher.EngineIface, st store.Store) *Server {
	return &Server{engine: engine, store: st}
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
type wsMessage struct {
	Type     string      `json:"type"`                // progress / sources / done / error
	Stage    string      `json:"stage,omitempty"`     // progress 专用：role/planning/searching/...
	Message  string      `json:"message,omitempty"`   // 人类可读说明
	Sources  []SourceDTO `json:"sources,omitempty"`   // sources/done 用
	Report   string      `json:"report,omitempty"`    // done 用：报告正文
	ReportID int64       `json:"report_id,omitempty"` // done 用：持久化后的报告 ID
}

// handleResearch 处理 WebSocket 升级后的研究通信。
//
// 协议（对齐 gpt-researcher 的 /ws 思路）：
//  1. 客户端连接后发一条 JSON：{"query":"研究问题"}
//  2. 服务端跑研究引擎，实时推送 progress 帧
//  3. 完成后推 sources + done（含报告全文与持久化 ID）
//  4. 失败推 error；任意一方可关闭连接
//
// WebSocket 是全双工长连接，没有 HTTP 的 idle 超时问题，
// 研究过程中的长静默期（LLM 推理几十秒）也不会断连。
func (s *Server) handleResearch(c *gin.Context) {
	conn, err := wsUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		// 升级失败时 conn 为 nil，gorilla 已写入错误响应。
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

	// 监听客户端断开（conn.ReadMessage 阻塞读，出错即视为断开）。
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				cancel()
				return
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
			Type:    "progress",
			Stage:   string(p.Stage),
			Message: p.Message,
		})
	}

	// 流式报告回调：把每个生成块实时推给前端。
	// 这样写报告期间连接持续有数据流动（report_chunk 帧），
	// 不会因"等完整大响应"被判 idle 超时；前端也能逐字渲染报告。
	onReportChunk := func(chunk, accu string) {
		if ctx.Err() != nil {
			return
		}
		safeWrite(wsMessage{Type: "report_chunk", Report: accu})
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
		report, err = s.engine.Run(ctx, req.Query, nil, onProgress, onReportChunk)
	}()
	if err != nil {
		if ctx.Err() != nil {
			return // 客户端断开，不必再发
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

	// 推送最终报告。
	safeWrite(wsMessage{Type: "done", Report: report.Markdown, Sources: srcDTOs, ReportID: reportID})
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
