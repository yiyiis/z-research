// Package api — revise.go 实现 /ws/revise 端点,支持报告的对话式修改。
//
// 与 /ws(研究端点)独立,互不影响:
//   - /ws: 客户端发 query,服务端跑研究引擎生成报告
//   - /ws/revise: 客户端发 report_id + instruction,服务端流式返回修改后的报告
//
// 协议(客户端 → 服务端首帧):
//
//	{
//	  "type": "revise_request",
//	  "report_id": 123,
//	  "instruction": "把结论改简洁点 / 补充最新的 MoE / 翻译成英文",
//	  "history": [{"role":"user","content":"..."},{"role":"assistant","content":"..."}]
//	}
//
// 协议(服务端 → 客户端):
//   - revise_progress: 修改进度(分类/检索/修改中)
//   - revise_sources: 新增来源(仅 supplement)
//   - revise_chunk: 流式修改报告(累积值,前端实时渲染)
//   - revise_done: 修改完成(含新 report_id + 最终报告)
//   - revise_error: 失败
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"

	"z-research/backend/internal/revise"
	"z-research/backend/internal/store"
)

// reviseRequest 是客户端发来的修改请求(首帧)。
type reviseRequest struct {
	Type        string           `json:"type"`         // 必须是 "revise_request"
	ReportID    int64            `json:"report_id"`    // 待修改的报告 ID
	Instruction string           `json:"instruction"`  // 修改指令
	History     []revise.Message `json:"history"`      // 多轮对话历史
}

// handleRevise 处理 /ws/revise 的 WebSocket 修改请求。
func (s *Server) handleRevise(c *gin.Context) {
	if s.reviseEngine == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "修改引擎未启用"})
		return
	}
	conn, err := wsUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()

	// 并发写锁(与研究端点同理,progress/done 多处写)。
	var writeMu sync.Mutex
	safeWrite := func(msg wsMessage) {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.WriteJSON(msg)
	}

	// 读首帧(修改请求)。
	_, msgBytes, err := conn.ReadMessage()
	if err != nil {
		return
	}
	var req reviseRequest
	if err := json.Unmarshal(msgBytes, &req); err != nil {
		safeWrite(wsMessage{Type: "revise_error", Message: "请求格式错误: " + err.Error()})
		return
	}
	if req.ReportID <= 0 || req.Instruction == "" {
		safeWrite(wsMessage{Type: "revise_error", Message: "report_id 和 instruction 不能为空"})
		return
	}

	// 从 store 读原报告。
	orig, err := s.store.Get(ctx, req.ReportID)
	if err != nil || orig == nil {
		safeWrite(wsMessage{Type: "revise_error", Message: fmt.Sprintf("找不到报告 #%d", req.ReportID)})
		return
	}
	// store.Source → revise.SourceRow。
	origSources := make([]revise.SourceRow, len(orig.Sources))
	for i, src := range orig.Sources {
		origSources[i] = revise.SourceRow{N: src.N, URL: src.URL, Title: src.Title}
	}

	// 流式 chunk 回调(与研究端点的 onReportChunk 同款,但 type 不同)。
	onChunk := func(chunk, accu string) {
		if ctx.Err() != nil {
			return
		}
		safeWrite(wsMessage{Type: "revise_chunk", Report: accu})
	}

	// 进度回调。
	onProgress := func(stage, message string) {
		if ctx.Err() != nil {
			return
		}
		safeWrite(wsMessage{Type: "revise_progress", Stage: stage, Message: message})
	}

	// defer recover 防 panic 断连(与研究端点同理)。
	var result *revise.Result
	func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("[PANIC] handleRevise: %v\n", r)
				safeWrite(wsMessage{Type: "revise_error", Message: fmt.Sprintf("内部错误: %v", r)})
				cancel()
			}
		}()
		result, err = s.reviseEngine.Revise(ctx, revise.Request{
			ReportID:    req.ReportID,
			Instruction: req.Instruction,
			History:     req.History,
		}, orig.Content, origSources, onProgress, onChunk)
	}()
	if err != nil {
		if ctx.Err() != nil {
			return // 客户端断开
		}
		safeWrite(wsMessage{Type: "revise_error", Message: err.Error()})
		return
	}

	// 推送新增来源(仅 supplement 有)。
	if len(result.NewSources) > 0 {
		srcDTOs := make([]SourceDTO, len(result.NewSources))
		for i, src := range result.NewSources {
			srcDTOs[i] = SourceDTO{N: src.N, URL: src.URL, Title: src.Title}
		}
		safeWrite(wsMessage{Type: "revise_sources", Sources: srcDTOs})
	}

	// 推送完成帧。
	safeWrite(wsMessage{
		Type:     "revise_done",
		Report:   result.NewReport,
		ReportID: result.SavedReportID,
		Stage:    result.Action.String(),
	})
}

// store.Source 辅助转换(避免 import 循环警告,确认 store 包被使用)。
var _ = store.Source{}
