// Package revise — revise.go 实现核心的流式报告修改。
//
// ReviseReport 调用 LLM ChatStream 流式输出修改后的报告,
// 模式与 researcher.WriteReport 一致(每个 chunk 通过 onChunk 回调推送)。
// 支持多轮对话历史 + 可选的补充资料。
package revise

import (
	"context"
	"fmt"
	"strings"

	"z-research/backend/internal/llm"
)

// ReportChunkFn 与 researcher.ReportChunkFn 签名一致(避免循环依赖,本地定义)。
// chunk 是本次新生成的一段文本,accu 是累积到当前的完整报告。
type ReportChunkFn func(chunk, accu string)

// ReviseReport 流式修改报告。
//
// 参数:
//   - originalReport: 当前报告全文
//   - instruction: 用户本轮的修改指令
//   - newContext: 补充检索得到的资料(空串=无补充,local_edit/restyle 场景为空)
//   - history: 多轮对话历史(首次修改为空)
//   - onChunk: 流式回调,每个 chunk 推送一次(accu 是累积完整报告)
//   - language: 目标语言(翻译场景用,如 "english";空=保持原语言)
//
// 用 smart 档位(修改需要理解全文 + 保留结构,fast 档位理解力不足)。
func ReviseReport(
	ctx context.Context,
	l *llm.LLM,
	originalReport, instruction, newContext string,
	history []Message,
	onChunk ReportChunkFn,
	language string,
) (string, error) {
	if l == nil {
		return "", fmt.Errorf("revise: LLM 为空")
	}
	if strings.TrimSpace(originalReport) == "" {
		return "", fmt.Errorf("revise: 原报告为空")
	}
	if strings.TrimSpace(instruction) == "" {
		return "", fmt.Errorf("revise: 修改指令为空")
	}

	system := ReviseSystemPrompt
	// 翻译场景:显式强调目标语言。
	if lang := strings.TrimSpace(language); lang != "" {
		system += fmt.Sprintf("\n\n本次修改的目标语言: %s", lang)
	}

	user := ReviseUserPrompt(originalReport, instruction, newContext, history)

	// 流式调用(与 researcher.WriteReport 同款模式)。
	ch, err := l.ChatStream(ctx, system, user)
	if err != nil {
		return "", fmt.Errorf("revise: 流式启动失败: %w", err)
	}
	var b strings.Builder
	for chunk := range ch {
		b.WriteString(chunk)
		if onChunk != nil {
			onChunk(chunk, b.String())
		}
	}
	report := b.String()
	if strings.TrimSpace(report) == "" {
		return "", fmt.Errorf("revise: LLM 返回空内容")
	}
	return report, nil
}
