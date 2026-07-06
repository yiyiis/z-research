package researcher

import (
	"context"
	"fmt"
	"strings"

	"z-research/backend/internal/llm"
	"z-research/backend/internal/prompts"
)

// RunDetailed 执行详细报告流程（多轮拆分）：
//
//	选角色 → 收集初步资料 → 生成大纲 → 串行逐章{独立检索+流式撰写} → 写引言 → 写结论 → 拼接
//
// 对标 gpt-researcher 的 detailed_report。各章独立检索（深度优先），
// 每章用 ChatStream 流式生成，边写边把累积全文推给前端（report_chunk）。
// 章节间通过 existingHeaders 去重，避免内容重复。
func (e *Engine) RunDetailed(ctx context.Context, query string, opts *Options, onProgress EventFn, onReportChunk ReportChunkFn) (*FinalReport, error) {
	emit := func(p Progress) {
		if onProgress != nil {
			onProgress(p)
		}
	}
	maxSections := e.cfg.MaxSections
	if maxSections <= 0 {
		maxSections = 4
	}
	wordsPerSection := e.cfg.WordsPerSection
	if wordsPerSection <= 0 {
		wordsPerSection = 800
	}

	// ---- 阶段 1：选角色 ----
	role, err := ChooseRole(ctx, e.llm, query)
	if err != nil {
		role = "你是一名严谨的研究助理，擅长基于资料客观地撰写研究报告。"
		emit(Progress{Stage: StageRole, Message: fmt.Sprintf("角色生成失败，使用默认角色: %v", err)})
	} else {
		emit(Progress{Stage: StageRole, Message: role})
	}

	// ---- 阶段 2：收集初步资料（用于生成更贴切的大纲）----
	emit(Progress{Stage: StagePlanning, Message: "正在收集初步资料以生成大纲…"})
	initialRes, err := e.researcher.Conduct(ctx, query, onProgress)
	if err != nil {
		return nil, fmt.Errorf("初步资料收集失败: %w", err)
	}

	// ---- 阶段 3：生成大纲 ----
	emit(Progress{Stage: StageOutline, Message: "正在生成报告大纲…"})
	title, sections, err := e.generateOutline(ctx, query, initialRes.Context, maxSections)
	if err != nil {
		return nil, fmt.Errorf("生成大纲失败: %w", err)
	}
	emit(Progress{Stage: StageOutline, Message: fmt.Sprintf("大纲生成完成：%s（共 %d 章）", title, len(sections))})

	// 收集所有来源（初步资料 + 各章资料去重合并）。
	allSources := dedupSources(initialRes.Sources)
	// fullReport 累积全文（引言 → 各章 → 结论），每个流式块都把累积值推给前端。
	var full strings.Builder
	// existingHeaders 用于章节间去重。
	existingHeaders := make([]string, 0, len(sections))

	// 流式追加辅助：把 chunk 写入 full，并把累积全文推给前端。
	streamAppend := func(ch <-chan string) error {
		for chunk := range ch {
			full.WriteString(chunk)
			if onReportChunk != nil {
				onReportChunk(chunk, full.String())
			}
		}
		return nil
	}

	// ---- 阶段 4：写引言 ----
	emit(Progress{Stage: StageWriting, Message: "正在撰写引言…"})
	introCh, err := e.llm.ChatStream(ctx,
		prompts.IntroSystemPrompt(role),
		prompts.IntroUserPrompt(query, title, outlineText(sections)),
	)
	if err == nil {
		if err := streamAppend(introCh); err != nil {
			return nil, err
		}
	}

	// ---- 阶段 5：串行撰写各章 ----
	for _, sec := range sections {
		emit(Progress{Stage: StageSection, SectionTitle: sec.Title, Message: fmt.Sprintf("正在研究章节：%s", sec.Title)})

		// 该章独立检索资料（深度优先）。
		secRes, err := e.researcher.Conduct(ctx, sec.Title+" "+sec.Desc, onProgress)
		if err != nil {
			// 单章检索失败不致命，用初步资料兜底。
			secRes = &Result{Context: initialRes.Context}
		}
		// 合并该章来源。
		allSources = mergeSources(allSources, secRes.Sources)

		emit(Progress{Stage: StageSection, SectionTitle: sec.Title, Message: fmt.Sprintf("正在撰写章节：%s", sec.Title)})
		secCh, err := e.llm.ChatStream(ctx,
			prompts.SectionSystemPrompt(role, existingHeaders),
			prompts.SectionUserPrompt(query, sec.Title, sec.Desc, secRes.Context, wordsPerSection),
		)
		if err != nil {
			return nil, fmt.Errorf("撰写章节 %q 失败: %w", sec.Title, err)
		}
		// 章节之间加分隔。
		if full.Len() > 0 {
			full.WriteString("\n\n")
			if onReportChunk != nil {
				onReportChunk("\n\n", full.String())
			}
		}
		if err := streamAppend(secCh); err != nil {
			return nil, err
		}
		existingHeaders = append(existingHeaders, sec.Title)
	}

	// ---- 阶段 6：写结论 ----
	emit(Progress{Stage: StageWriting, Message: "正在撰写结论…"})
	conclCh, err := e.llm.ChatStream(ctx,
		prompts.ConclusionSystemPrompt(role),
		prompts.ConclusionUserPrompt(query, full.String()),
	)
	if err == nil {
		full.WriteString("\n\n")
		if onReportChunk != nil {
			onReportChunk("\n\n", full.String())
		}
		if err := streamAppend(conclCh); err != nil {
			return nil, err
		}
	}

	report := full.String()
	if strings.TrimSpace(report) == "" {
		return nil, fmt.Errorf("报告内容为空")
	}

	// 补来源列表（若 LLM 未自带）。
	if !strings.Contains(report, "参考资料") && len(allSources) > 0 {
		report = strings.TrimRight(report, "\n") + "\n\n## 参考资料\n"
		for _, s := range allSources {
			report += fmt.Sprintf("%d. %s — %s\n", s.N, s.Title, s.URL)
		}
	}

	return &FinalReport{Markdown: report, Sources: allSources}, nil
}

// generateOutline 让 LLM 生成报告大纲（JSON），解析为 title + sections。
func (e *Engine) generateOutline(ctx context.Context, query, initialResearch string, n int) (string, []OutlineSection, error) {
	var out struct {
		Title    string           `json:"title"`
		Sections []OutlineSection `json:"sections"`
	}
	// 用 strategic 档位（生成大纲=深度规划/拆子主题，gpt-r 的质量杠杆点）。
	if err := e.llm.StrategicChatJSON(ctx,
		prompts.OutlineSystemPrompt(),
		prompts.OutlineUserPrompt(query, initialResearch, n),
		&out,
	); err != nil {
		return "", nil, err
	}
	if out.Title == "" {
		out.Title = "研究报告"
	}
	// 清洗：去空、去伪章节（引言/结论等）、封顶 n。
	cleaned := make([]OutlineSection, 0, len(out.Sections))
	for _, s := range out.Sections {
		t := strings.TrimSpace(s.Title)
		if t == "" || isPseudoSection(t) {
			continue
		}
		s.Title = t
		if strings.TrimSpace(s.Desc) == "" {
			s.Desc = t
		}
		cleaned = append(cleaned, s)
		if len(cleaned) >= n {
			break
		}
	}
	if len(cleaned) == 0 {
		return "", nil, fmt.Errorf("大纲无有效章节（原始输出: %s）", llm.Truncate(fmt.Sprintf("%v", out.Sections), 200))
	}
	return out.Title, cleaned, nil
}

// isPseudoSection 判断是否为通用/伪章节（不应出现在大纲里）。
func isPseudoSection(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "引言", "introduction", "intro", "导言", "概述", "概要",
		"结论", "conclusion", "结语", "总结",
		"参考资料", "参考文献", "references", "reference":
		return true
	}
	return false
}

// outlineText 把 sections 渲染为可读的大纲文本（供引言 prompt 用）。
func outlineText(sections []OutlineSection) string {
	var b strings.Builder
	for i, s := range sections {
		fmt.Fprintf(&b, "%d. %s\n   %s\n", i+1, s.Title, s.Desc)
	}
	return b.String()
}

// dedupSources 按去空后的列表返回（不重编号，保留原 N）。
func dedupSources(in []Source) []Source {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]Source, 0, len(in))
	for _, s := range in {
		key := s.URL
		if key == "" {
			continue
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, s)
	}
	return out
}

// mergeSources 把 newSrcs 合并到 existing（按 URL 去重），并重新连续编号。
func mergeSources(existing []Source, newSrcs []Source) []Source {
	seen := make(map[string]bool, len(existing))
	out := make([]Source, 0, len(existing)+len(newSrcs))
	for _, s := range existing {
		if s.URL != "" && !seen[s.URL] {
			seen[s.URL] = true
			out = append(out, s)
		}
	}
	for _, s := range newSrcs {
		if s.URL != "" && !seen[s.URL] {
			seen[s.URL] = true
			out = append(out, s)
		}
	}
	// 重新连续编号（1..N）。
	for i := range out {
		out[i].N = i + 1
	}
	return out
}
