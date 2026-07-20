package revise

import (
	"strings"
	"testing"
)

func TestAction_String(t *testing.T) {
	cases := map[Action]string{
		ActionSupplement: "supplement",
		ActionLocalEdit:  "local_edit",
		ActionRestyle:    "restyle",
		ActionUnknown:    "unknown",
	}
	for a, want := range cases {
		if got := a.String(); got != want {
			t.Errorf("%v.String() = %q, want %q", a, got, want)
		}
	}
}

func TestMergeSources_NewNumberedSequentially(t *testing.T) {
	original := []SourceRow{
		{N: 1, URL: "https://a.com", Title: "A"},
		{N: 2, URL: "https://b.com", Title: "B"},
	}
	additional := []SourceRow{
		{N: 1, URL: "https://c.com", Title: "C"}, // 新来源(原编号无关紧要,会被重编)
		{N: 2, URL: "https://d.com", Title: "D"},
	}
	merged := mergeSources(original, additional)
	if len(merged) != 4 {
		t.Fatalf("merged len = %d, want 4: %+v", len(merged), merged)
	}
	// 原来源编号不变。
	if merged[0].N != 1 || merged[1].N != 2 {
		t.Errorf("original renumbered: %+v %+v", merged[0], merged[1])
	}
	// 新来源编号顺延 3, 4。
	if merged[2].N != 3 || merged[3].N != 4 {
		t.Errorf("additional not sequential: %+v %+v", merged[2], merged[3])
	}
}

func TestMergeSources_Dedup(t *testing.T) {
	original := []SourceRow{
		{N: 1, URL: "https://a.com", Title: "A"},
	}
	additional := []SourceRow{
		{N: 1, URL: "https://a.com", Title: "A dup"}, // 重复,应跳过
		{N: 2, URL: "https://b.com", Title: "B"},
	}
	merged := mergeSources(original, additional)
	if len(merged) != 2 {
		t.Fatalf("dedup failed, len = %d: %+v", len(merged), merged)
	}
}

func TestMergeSources_NoAdditional(t *testing.T) {
	original := []SourceRow{{N: 1, URL: "https://a.com", Title: "A"}}
	merged := mergeSources(original, nil)
	if len(merged) != 1 || merged[0].N != 1 {
		t.Errorf("no additional should return original as-is: %+v", merged)
	}
}

func TestExtractTitle(t *testing.T) {
	cases := []struct {
		name    string
		markdown string
		want    string
	}{
		{"has_title", "# 报告标题\n\n正文", "报告标题"},
		{"title_with_extra_space", "#   标题  \n正文", "标题"},
		{"no_title", "正文无标题", "修订报告"},
		{"h2_not_h1", "## 二级标题\n# 一级标题", "一级标题"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractTitle(tc.markdown); got != tc.want {
				t.Errorf("extractTitle() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestReviseUserPrompt_ContainsKeyInfo(t *testing.T) {
	p := ReviseUserPrompt("# 原报告", "把结论改简洁", "", nil)
	for _, want := range []string{"原报告", "把结论改简洁", "完整"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

func TestReviseUserPrompt_WithNewContext(t *testing.T) {
	p := ReviseUserPrompt("# 原报告", "补充 X", "新资料: blah", nil)
	if !strings.Contains(p, "补充检索到的新资料") || !strings.Contains(p, "新资料: blah") {
		t.Error("newContext 应出现在 prompt 里")
	}
}

func TestReviseUserPrompt_WithHistory(t *testing.T) {
	history := []Message{
		{Role: "user", Content: "第一次说改简洁"},
		{Role: "assistant", Content: "# 修改后的报告 v1"},
	}
	p := ReviseUserPrompt("# 原报告", "再精简点", "", history)
	if !strings.Contains(p, "之前的修改对话历史") {
		t.Error("应包含对话历史段")
	}
	if !strings.Contains(p, "第一次说改简洁") {
		t.Error("应包含 user 历史消息")
	}
}

func TestClassifyUserPrompt_TruncatesReport(t *testing.T) {
	longReport := strings.Repeat("x", 1000)
	p := ClassifyUserPrompt("补充 X", longReport)
	// 报告摘要应被截断到 500 rune。
	if strings.Count(p, "x") > 600 {
		t.Errorf("report excerpt not truncated: %d x's", strings.Count(p, "x"))
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("hello", 10); got != "hello" {
		t.Errorf("short string should not truncate: %q", got)
	}
	if got := truncateRunes("hello world", 5); got != "hello…" {
		t.Errorf("long string should truncate: %q", got)
	}
}
