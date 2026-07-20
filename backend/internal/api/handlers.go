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
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"z-research/backend/internal/eval"
	"z-research/backend/internal/llm"
	"z-research/backend/internal/researcher"
	"z-research/backend/internal/revise"
	"z-research/backend/internal/store"
)

// Server 持有所有依赖，注册路由。
//
// 四引擎：single（确定性工作流）+ multi（多智能体图）+ react（ReAct Agent）
// + deep（深度递归），按请求 opts.Mode 路由。
// multi/react/deep 可能为 nil（构造失败），前端选时报错。
type Server struct {
	singleEngine researcher.EngineIface
	multiEngine  researcher.EngineIface // 可为 nil
	reactEngine  researcher.EngineIface // 可为 nil（ReAct Agent）
	deepEngine   researcher.EngineIface // 可为 nil（深度递归）
	store        store.Store
	// llm 用于在 done 帧里拿 token 用量快照（流量计费）。
	llm *llm.LLM
	// evalStore 持久化 LLM-as-Judge 评估分数，nil 表示禁用评估。
	evalStore *store.SQLiteEvaluationStore
	// evaluateOnDone 控制是否在 done 后自动评估（来自 cfg.EvalOnDone）。
	evaluateOnDone bool
	// reviseEngine 是报告对话式修改引擎，nil 表示禁用修改功能。
	reviseEngine *revise.Engine
}

// NewServer 创建 HTTP 服务。
func NewServer(single, multi, react, deep researcher.EngineIface, st store.Store, llmClient *llm.LLM, evalStore *store.SQLiteEvaluationStore, evaluateOnDone bool, reviseEng *revise.Engine) *Server {
	return &Server{
		singleEngine:  single,
		multiEngine:   multi,
		reactEngine:   react,
		deepEngine:    deep,
		store:         st,
		llm:           llmClient,
		evalStore:     evalStore,
		evaluateOnDone: evaluateOnDone,
		reviseEngine:  reviseEng,
	}
}

// pickEngine 按 Mode 选引擎。
func (s *Server) pickEngine(mode string) (researcher.EngineIface, bool) {
	switch mode {
	case "multi":
		if s.multiEngine == nil {
			return nil, false
		}
		return s.multiEngine, true
	case "react":
		if s.reactEngine == nil {
			return nil, false
		}
		return s.reactEngine, true
	case "deep":
		if s.deepEngine == nil {
			return nil, false
		}
		return s.deepEngine, true
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

// modeOrSingle 把空 mode 显示为 "single"，用于错误文案。
func modeOrSingle(mode string) string {
	if mode == "" {
		return "single"
	}
	return mode
}

// friendlyError 把内部错误转成用户友好的中文消息。
//
// 设计目标：
//   - 不暴露敏感信息（API key、内部路径、stack trace）给前端。
//   - 把常见错误归类成可操作的提示（鉴权/限流/网络/模型问题）。
//   - 保留原始错误的关键部分供调试（脱敏后）。
func friendlyError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	kind := llm.ClassifyError(err)
	switch kind {
	case llm.ErrAuth:
		// 鉴权失败：不暴露具体 key 信息，只提示配置问题。
		return "LLM 鉴权失败：请检查 .env 里的 API Key 是否正确（或是否过期）"
	case llm.ErrTransient:
		// 瞬时错误：已重试仍失败，提示稍后重试。
		if strings.Contains(strings.ToLower(msg), "429") || strings.Contains(strings.ToLower(msg), "rate") {
			return "LLM 调用被限流（429），请稍后重试或降低并发"
		}
		return "网络或 LLM 服务暂时不可用（已自动重试仍失败），请稍后重试"
	case llm.ErrClient:
		// 客户端错误：通常是模型名或参数问题。
		return "LLM 请求参数错误：" + truncateMsg(msg, 100)
	default:
		// 未分类错误：截断长度避免泄露过多内部细节。
		return truncateMsg(msg, 200)
	}
}

// truncateMsg 把错误消息截断到 maxRune 字符，超长加省略号。
func truncateMsg(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
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
	Type         string             `json:"type"`                    // 帧语义
	Stage        string             `json:"stage,omitempty"`         // progress 专用
	Message      string             `json:"message,omitempty"`       // 人类可读说明
	SectionTitle string             `json:"section_title,omitempty"` // progress 专用：当前章节（详细报告）
	Sources      []SourceDTO        `json:"sources,omitempty"`       // sources/done 用
	Report       string             `json:"report,omitempty"`        // report_chunk/done 用：报告片段或全文
	ReportID     int64              `json:"report_id,omitempty"`     // done 用：持久化后的报告 ID
	Usage        *llm.UsageSnapshot `json:"usage,omitempty"`         // done 用：token 用量（流量计费）
	Evaluation   *eval.ScoreDTO     `json:"evaluation,omitempty"`    // evaluation 帧：LLM-as-Judge 评分

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
	} else if req.Mode == "deep" {
		// 深度递归：per-run breadth/depth（前端可调）。
		opts.Breadth = req.Breadth
		opts.Depth = req.Depth
	}
	// If mode is empty, opts.Mode is nil → engine fallbacks to default (cfg.Mode).

	// 按 req.Mode 选引擎（multi/react/deep 可能为 nil）。
	engine, ok := s.pickEngine(req.Mode)
	if !ok {
		safeWrite(wsMessage{Type: "error", Message: fmt.Sprintf("%s 模式不可用（构造失败或未启动），请改用 single", modeOrSingle(req.Mode))})
		return
	}

	// 跑研究引擎（用 defer recover 防止 panic 直接断连）。
	// 研究开始前 Reset token 用量统计器（避免累积多次研究的用量）。
	if s.llm != nil {
		s.llm.Usage().Reset()
	}
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
		safeWrite(wsMessage{Type: "error", Message: friendlyError(err)})
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

	// 流量计费：从 LLM collector 拿本次研究的 token 用量快照。
	// 每次研究开始前会 Reset collector（见下方引擎调用前），所以这里是本次的用量。
	var usageSnap *llm.UsageSnapshot
	if s.llm != nil {
		snap := s.llm.Usage().Snapshot()
		usageSnap = &snap
		// 顺带把 usage 也填进 FinalReport（供需要 FinalReport 的下游使用）。
		report.Usage = usageSnap
		// CLI 风格的汇总日志。
		fmt.Fprintf(os.Stderr, "📊 %s\n", s.llm.Usage().Summary())
	}

	safeWrite(wsMessage{Type: "done", Report: report.Markdown, Sources: srcDTOs, ReportID: reportID, Usage: usageSnap})

	// LLM-as-Judge 自动评估（done 帧之后）。
	// 评估是"锦上添花"：失败不致命，不报错给前端（只是不展示评分）。
	// done 帧先发，让前端立即展示报告；评估完成后再发 evaluation 帧，前端刷新评分卡片。
	// 连接保持到评估完成（或超时），评估通常比生成报告快得多。
	if s.evaluateOnDone && s.evalStore != nil && s.llm != nil && reportID > 0 {
		s.runEvaluation(ctx, req.Query, report.Markdown, report.Sources, reportID, safeWrite)
	}
}

// runEvaluation 调用 LLM-as-Judge 给报告打分，持久化结果，并通过 WS 推送 evaluation 帧。
// 失败时静默（只记日志），不影响主流程。带超时保护（评估卡住不拖死连接）。
func (s *Server) runEvaluation(parentCtx context.Context, query, reportMarkdown string,
	sources []researcher.Source, reportID int64, safeWrite func(wsMessage)) {

	// 评估用独立 ctx（带超时），避免 parentCtx 取消时评估半途而废。
	// 但若 parentCtx 已取消（客户端断开），就别评估了（推也没人收）。
	if parentCtx.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// 把 researcher.Source 转成 eval.SourceRow（解耦）。
	rows := make([]eval.SourceRow, len(sources))
	for i, src := range sources {
		rows[i] = eval.SourceRow{N: src.N, URL: src.URL, Title: src.Title}
	}

	score, err := eval.JudgeReport(ctx, s.llm, query, reportMarkdown, rows)
	if err != nil {
		// 评估失败：记日志，不推帧（前端不展示评分即可）。
		fmt.Fprintf(os.Stderr, "⚠️  评估失败 (report %d): %v\n", reportID, err)
		return
	}

	// 持久化评估结果（失败不致命，仍推帧给前端）。
	scoreJSON, _ := json.Marshal(score)
	if _, err := s.evalStore.SaveEvaluation(parentCtx, store.SaveEvaluationInput{
		ReportID:  reportID,
		ScoreJSON: string(scoreJSON),
		Overall:   score.Overall(),
	}); err != nil {
		fmt.Fprintf(os.Stderr, "⚠️  评估持久化失败 (report %d): %v\n", reportID, err)
	}

	// 推 evaluation 帧（带扁平化 DTO，便于前端渲染）。
	safeWrite(wsMessage{Type: "evaluation", ReportID: reportID, Evaluation: score.ToDTO().Ptr()})
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
