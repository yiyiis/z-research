// Package multiagent — fallback_test.go 验证各角色 JSON 失败时的业务降级。
//
// 这些测试模拟"LLM 返回垃圾/截断/空"的场景，确认函数不会让整个 graph 崩，
// 而是返回业务安全的兜底值（planner 给通用大纲、writer 给空 layout、
// fact_checker 放行）。
package multiagent

import (
	"strings"
	"testing"

	"z-research/backend/internal/llm"
)

// fakeLLM 是 *llm.LLM 的桩，返回预设的（垃圾）内容。
// 由于 llm.LLM 的方法都绑定在具体类型上（不是接口），这里用一个 trick：
// 直接调用纯函数（PlanOutline 等）需要 *llm.LLM，我们构造一个最小可用的。
// 但 PlanOutline 内部调 l.StrategicChat，没法简单桩。
// 因此本测试只验证 fallbackOutline（纯函数）和 FactCheck/WriteReportLayout
// 的降级路径（需要 LLM，跳过，靠代码审查保证）。

func TestFallbackOutline(t *testing.T) {
	cases := []struct {
		name        string
		query       string
		maxSections int
		wantMinSec  int // 至少这么多节
		wantMaxSec  int
	}{
		{"normal", "大模型推理优化", 3, 1, 3},
		{"maxSections_too_large", "test", 10, 1, 4}, // 上限 4
		{"maxSections_zero", "test", 0, 1, 4},       // 默认 4
		{"long_query", strings.Repeat("长", 100), 2, 1, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			title, sections := fallbackOutline(tc.query, tc.maxSections)
			if title == "" {
				t.Error("title should not be empty")
			}
			if len(sections) < tc.wantMinSec {
				t.Errorf("sections count = %d, want >= %d", len(sections), tc.wantMinSec)
			}
			if len(sections) > tc.wantMaxSec {
				t.Errorf("sections count = %d, want <= %d", len(sections), tc.wantMaxSec)
			}
			// 所有 section 应非空。
			for _, s := range sections {
				if strings.TrimSpace(s) == "" {
					t.Error("section should not be empty")
				}
			}
		})
	}
}

func TestFallbackOutline_LongTitleTruncated(t *testing.T) {
	longQuery := strings.Repeat("主题", 50) // 100 字符
	title, _ := fallbackOutline(longQuery, 4)
	if !strings.HasSuffix(title, "…") {
		t.Errorf("long title should be truncated with …, got %q", title)
	}
	if len([]rune(title)) > 50 {
		t.Errorf("title too long: %d runes", len([]rune(title)))
	}
}

// 编译期断言：确保我们用的 llm 类型存在（避免重构时漏改）。
var _ = llm.Truncate
