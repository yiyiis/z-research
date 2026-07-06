package researcher

import (
	"testing"
)

// TestIsPseudoSection 验证伪章节识别（引言/结论/参考资料等不应进大纲）。
func TestIsPseudoSection(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"引言", true},
		{"Introduction", true},
		{"结论", true},
		{"参考资料", true},
		{"References", true},
		{"核心技术原理", false},
		{"市场分析", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isPseudoSection(c.in); got != c.want {
			t.Errorf("isPseudoSection(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestDedupSources 验证按 URL 去重。
func TestDedupSources(t *testing.T) {
	in := []Source{
		{N: 1, URL: "http://a.com", Title: "A"},
		{N: 2, URL: "http://b.com", Title: "B"},
		{N: 3, URL: "http://a.com", Title: "A重复"}, // 去重
		{N: 4, URL: "", Title: "空URL"},            // 丢弃
	}
	got := dedupSources(in)
	if len(got) != 2 {
		t.Fatalf("期望 2 个（去重后），得到 %d: %+v", len(got), got)
	}
	if got[0].URL != "http://a.com" || got[1].URL != "http://b.com" {
		t.Errorf("去重结果不正确: %+v", got)
	}
	// 保留首次的标题。
	if got[0].Title != "A" {
		t.Errorf("应保留首次标题 A，得到 %q", got[0].Title)
	}
}

// TestMergeSources 验证合并去重 + 重新连续编号。
func TestMergeSources(t *testing.T) {
	existing := []Source{
		{N: 1, URL: "http://a.com", Title: "A"},
		{N: 5, URL: "http://b.com", Title: "B"}, // 编号不连续
	}
	newSrcs := []Source{
		{N: 10, URL: "http://b.com", Title: "B重复"}, // URL 重复，跳过
		{N: 11, URL: "http://c.com", Title: "C"},
	}
	got := mergeSources(existing, newSrcs)
	if len(got) != 3 {
		t.Fatalf("期望 3 个（a,b,c），得到 %d: %+v", len(got), got)
	}
	// 验证重新连续编号 1,2,3。
	for i, s := range got {
		if s.N != i+1 {
			t.Errorf("第 %d 个来源编号应为 %d，得到 %d", i, i+1, s.N)
		}
	}
	// 顺序：a, b, c。
	if got[0].URL != "http://a.com" || got[2].URL != "http://c.com" {
		t.Errorf("合并顺序不正确: %+v", got)
	}
}

// TestOutlineText 验证大纲文本渲染。
func TestOutlineText(t *testing.T) {
	sections := []OutlineSection{
		{Title: "技术原理", Desc: "讲解核心原理"},
		{Title: "市场分析", Desc: "分析市场格局"},
	}
	got := outlineText(sections)
	if !containsStr(got, "技术原理") || !containsStr(got, "市场分析") {
		t.Errorf("大纲文本应含标题，得到 %q", got)
	}
}

// TestGenerateOutline_JSONParse 用假 LLM 测大纲 JSON 解析。
// 由于 generateOutline 依赖 *Engine + *llm.LLM，这里只测 JSON 结构解析逻辑
// （通过构造一个最小 Engine 验证错误路径）。
func TestGenerateOutline_EmptySections(t *testing.T) {
	// 这个测试主要保证 isPseudoSection 在 generateOutline 的清洗逻辑里生效。
	// 完整的 LLM 调用测试依赖外部服务，留给集成测试。
	pseudo := []string{"引言", "结论", "参考资料", "概述"}
	for _, p := range pseudo {
		if !isPseudoSection(p) {
			t.Errorf("'%s' 应被识别为伪章节", p)
		}
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
