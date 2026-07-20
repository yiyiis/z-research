// Package revise — engine.go 编排完整的修改流程。
//
// 流程:
//  1. 从 store 读原报告(按 ReportID)
//  2. 用 fast 档分类指令(supplement/local_edit/restyle)
//  3. 若 supplement: 调 researcher.Conduct 补充检索,拿新 context + sources
//  4. 调 ReviseReport 流式修改(带新 context + 历史)
//  5. 把新 sources 合并进原 sources(编号顺延)
//  6. 另存为新报告(不覆盖原报告,query 加 [修订自 #N] 后缀)
//
// 与 researcher.EngineIface 的区别:不实现该接口(语义不同,revise 是修改不是研究)。
package revise

import (
	"context"
	"fmt"
	"strings"

	"z-research/backend/internal/config"
	"z-research/backend/internal/llm"
	"z-research/backend/internal/researcher"
	"z-research/backend/internal/store"
)

type Engine struct {
	cfg        *config.Config
	llm        *llm.LLM
	researcher *researcher.Researcher // 复用做补充检索
	store      store.Store            // 读原报告 + 存新报告
}

// NewEngine 创建修改引擎。
func NewEngine(cfg *config.Config, l *llm.LLM, r *researcher.Researcher, st store.Store) *Engine {
	return &Engine{cfg: cfg, llm: l, researcher: r, store: st}
}

// ProgressFn 是修改过程中的进度回调(与研究引擎的 EventFn 语义一致)。
type ProgressFn func(stage, message string)

// Revise 执行一次报告修改。
//
// onProgress 上报各阶段(分类/检索/修改);onChunk 流式推送修改中的报告。
// 返回修改结果(含新报告 + 新 sources + 持久化后的 ID)。
func (e *Engine) Revise(
	ctx context.Context,
	req Request,
	originalReport string,
	originalSources []SourceRow,
	onProgress ProgressFn,
	onChunk ReportChunkFn,
) (*Result, error) {
	emit := func(stage, msg string) {
		if onProgress != nil {
			onProgress(stage, msg)
		}
	}

	emit("classify", "正在分析修改指令...")
	cls, err := ClassifyInstruction(ctx, e.llm, req.Instruction, originalReport)
	if err != nil {
		return nil, fmt.Errorf("分类失败: %w", err)
	}
	emit("classify", fmt.Sprintf("指令类型: %s", cls.Action))

	var (
		newContext   string
		newSources   []SourceRow
		mergedSources = originalSources
	)

	// 补充检索路径(仅 supplement)。
	if cls.Action == ActionSupplement && e.researcher != nil {
		emit("searching", fmt.Sprintf("补充检索: %s", cls.SearchQuery))
		res, err := e.researcher.Conduct(ctx, cls.SearchQuery, func(p researcher.Progress) {
			// 转发检索进度(让前端看到在搜什么)。
			msg := p.Message
			if msg == "" {
				msg = fmt.Sprintf("subquery=%s", p.SubQuery)
			}
			emit("searching", msg)
		})
		if err != nil {
			// 检索失败不致命:降级为不带新资料的局部修改。
			emit("searching", fmt.Sprintf("补充检索失败,降级为局部修改: %v", err))
		} else {
			newContext = res.Context
			// 把新 sources 转成 SourceRow。
			for _, s := range res.Sources {
				newSources = append(newSources, SourceRow{N: s.N, URL: s.URL, Title: s.Title})
			}
			// 合并新旧 sources(新来源编号顺延)。
			mergedSources = mergeSources(originalSources, newSources)
			emit("searching", fmt.Sprintf("补充检索完成,新增 %d 个来源", len(newSources)))
		}
	}

	// 流式修改。
	emit("revising", "正在修改报告...")
	newReport, err := ReviseReport(ctx, e.llm, originalReport, req.Instruction, newContext, req.History, onChunk, "")
	if err != nil {
		return nil, fmt.Errorf("修改失败: %w", err)
	}
	emit("revising", "修改完成")

	// 持久化:另存为新报告(不覆盖原报告)。
	// query 加后缀标注来源,前端在历史列表区分。
	savedID, err := e.saveRevision(ctx, req.ReportID, newReport, mergedSources)
	if err != nil {
		// 持久化失败不致命:返回结果但 SavedReportID=0(前端提示"未保存")。
		emit("error", fmt.Sprintf("保存失败: %v", err))
	}

	return &Result{
		NewReport:     newReport,
		NewSources:    newSources,
		SavedReportID: savedID,
		Action:        cls.Action,
	}, nil
}

// saveRevision 把修改后的报告另存为新记录。
//
// 策略(零 schema 变更):
//   - query 用 "[修订自 #原ID] 原query" 格式,前端在历史列表据此识别修订版
//   - 标题从新报告抽取(首个 # 行)
//   - sources 合并后存(新来源编号已顺延)
func (e *Engine) saveRevision(ctx context.Context, originalID int64, newReport string, sources []SourceRow) (int64, error) {
	if e.store == nil {
		return 0, fmt.Errorf("store 未配置")
	}
	// 读原 query(用于拼接修订标识)。
	var originalQuery string
	if orig, err := e.store.Get(ctx, originalID); err == nil && orig != nil {
		originalQuery = orig.Query
	}
	// 拼接新 query。
	newQuery := originalQuery
	if newQuery == "" {
		newQuery = "(修订报告)"
	}
	newQuery = fmt.Sprintf("[修订自 #%d] %s", originalID, newQuery)

	title := extractTitle(newReport)
	storeSources := make([]store.Source, len(sources))
	for i, s := range sources {
		storeSources[i] = store.Source{N: s.N, URL: s.URL, Title: s.Title}
	}

	saved, err := e.store.Save(ctx, newQuery, title, newReport, storeSources)
	if err != nil {
		return 0, fmt.Errorf("保存修订报告: %w", err)
	}
	return saved.ID, nil
}

// mergeSources 合并原 sources 和新 sources,新来源编号顺延。
// 原 sources 编号保持不变(报告正文里的 [n] 引用仍有效),新 sources 从 max+1 开始编号。
func mergeSources(original, additional []SourceRow) []SourceRow {
	if len(additional) == 0 {
		return original
	}
	// 找原 sources 的最大编号。
	maxN := 0
	for _, s := range original {
		if s.N > maxN {
			maxN = s.N
		}
	}
	// 新 sources 重新编号(顺延)。
	out := make([]SourceRow, 0, len(original)+len(additional))
	out = append(out, original...)
	seen := make(map[string]bool, len(original))
	for _, s := range original {
		seen[s.URL] = true
	}
	for _, s := range additional {
		if seen[s.URL] {
			continue // 去重(检索可能命中已有 URL)
		}
		seen[s.URL] = true
		maxN++
		out = append(out, SourceRow{N: maxN, URL: s.URL, Title: s.Title})
	}
	return out
}

// extractTitle 从 Markdown 抽取首个 # 行作为标题。
func extractTitle(markdown string) string {
	for _, line := range strings.Split(markdown, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return "修订报告"
}
