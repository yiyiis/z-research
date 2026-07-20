// Package revise — classify.go 实现修改指令的分类。
//
// 用 fast 档位 LLM 把用户指令分成 3 类(supplement/local_edit/restyle),
// 决定后续走哪条处理路径。分类失败时保守归为 local_edit(直接改,不检索)。
package revise

import (
	"context"
	"fmt"
	"strings"

	"z-research/backend/internal/llm"
)

// ClassifyResult 是分类的产出。
type ClassifyResult struct {
	Action     Action
	SearchQuery string // 仅 Action=supplement 时有意义
}

// ClassifyInstruction 用 LLM 把用户指令分类。
//
// reportExcerpt 是当前报告的摘要(帮助 LLM 理解上下文,如"补充 X"的 X 是否已存在)。
// 用 fast 档位(分类是小任务,要求快)。
//
// 失败时返回 ActionLocalEdit(保守:直接改比误触发检索成本低)。
func ClassifyInstruction(ctx context.Context, l *llm.LLM, instruction, reportExcerpt string) (*ClassifyResult, error) {
	if l == nil {
		return &ClassifyResult{Action: ActionLocalEdit}, nil
	}
	if strings.TrimSpace(instruction) == "" {
		return nil, fmt.Errorf("classify: 指令为空")
	}

	var raw struct {
		Action      string `json:"action"`
		SearchQuery string `json:"search_query"`
	}
	if err := l.FastChatJSONRole(ctx, "revise_classifier",
		ClassifySystemPrompt,
		ClassifyUserPrompt(instruction, reportExcerpt),
		&raw); err != nil {
		// 分类失败:保守归为 local_edit(不检索,直接改)。
		return &ClassifyResult{Action: ActionLocalEdit}, nil
	}

	res := &ClassifyResult{
		SearchQuery: strings.TrimSpace(raw.SearchQuery),
	}
	switch strings.ToLower(strings.TrimSpace(raw.Action)) {
	case "supplement":
		res.Action = ActionSupplement
		// supplement 但没给 search_query:用原 instruction 兜底。
		if res.SearchQuery == "" {
			res.SearchQuery = instruction
		}
	case "restyle":
		res.Action = ActionRestyle
	case "local_edit":
		res.Action = ActionLocalEdit
	default:
		// 未识别:保守 local_edit。
		res.Action = ActionLocalEdit
	}
	return res, nil
}
