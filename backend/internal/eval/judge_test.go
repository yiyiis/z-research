package eval

import (
	"testing"
)

func TestScore_Overall(t *testing.T) {
	cases := []struct {
		name string
		score *Score
		want  float64
	}{
		{"nil", nil, 0},
		{"empty", &Score{Dimensions: map[string]DimensionScore{}}, 0},
		{"single", &Score{Dimensions: map[string]DimensionScore{
			"coverage": {Score: 8},
		}}, 8},
		{"average", &Score{Dimensions: map[string]DimensionScore{
			"coverage":    {Score: 10},
			"citation":    {Score: 6},
			"structure":   {Score: 8},
			"objectivity": {Score: 7},
			"readability": {Score: 9},
		}}, 8}, // (10+6+8+7+9)/5 = 8
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.score.Overall()
			if abs(got-tc.want) > 0.01 {
				t.Errorf("Overall() = %.2f, want %.2f", got, tc.want)
			}
		})
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func TestJudgeUserPrompt_ContainsKeyInfo(t *testing.T) {
	sources := []SourceRow{
		{N: 1, URL: "https://a.com", Title: "A"},
		{N: 2, URL: "https://b.com", Title: "B"},
	}
	p := JudgeUserPrompt("什么是 RAG", "# RAG 报告\n正文...", sources)
	for _, want := range []string{"什么是 RAG", "RAG 报告", "[1] A", "[2] B", "https://a.com"} {
		if !contains(p, want) {
			t.Errorf("JudgeUserPrompt 缺少 %q", want)
		}
	}
}

func TestJudgeUserPrompt_NoSources(t *testing.T) {
	p := JudgeUserPrompt("q", "report", nil)
	if !contains(p, "(无来源)") {
		t.Error("无来源时应显示 (无来源)")
	}
}

func TestJudgeUserPrompt_LongReportTruncated(t *testing.T) {
	// 构造超长报告(> 8000 rune)。
	longReport := ""
	for i := 0; i < 10000; i++ {
		longReport += "x"
	}
	p := JudgeUserPrompt("q", longReport, nil)
	if !contains(p, "中间部分省略") {
		t.Error("超长报告应被截断并标注省略")
	}
}

func TestLabel(t *testing.T) {
	cases := map[string]string{
		"coverage":    "完整性",
		"citation":    "引用质量",
		"structure":   "结构组织",
		"objectivity": "客观性",
		"readability": "可读性",
		"unknown":     "unknown", // 未知维度返回原名
	}
	for in, want := range cases {
		if got := Label(in); got != want {
			t.Errorf("Label(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestScore_ToDTO(t *testing.T) {
	s := &Score{
		Dimensions: map[string]DimensionScore{
			"coverage":    {Score: 9, Note: "全面"},
			"citation":    {Score: 5, Note: "引用不足"},
			"structure":   {Score: 8, Note: ""},
			"objectivity": {Score: 7, Note: "基本客观"},
			"readability": {Score: 9, Note: "流畅"},
		},
		Summary: "整体优秀,但引用需加强",
	}
	dto := s.ToDTO()

	if abs(dto.Overall-7.6) > 0.01 {
		t.Errorf("Overall = %.2f, want 7.6", dto.Overall)
	}
	if dto.Summary != "整体优秀,但引用需加强" {
		t.Errorf("Summary = %q", dto.Summary)
	}
	// 维度应按 defaultDimensions 顺序排列。
	if len(dto.Dimensions) != 5 {
		t.Fatalf("Dimensions count = %d, want 5", len(dto.Dimensions))
	}
	expectedOrder := []string{"coverage", "citation", "structure", "objectivity", "readability"}
	for i, want := range expectedOrder {
		if dto.Dimensions[i].Name != want {
			t.Errorf("Dimensions[%d].Name = %q, want %q", i, dto.Dimensions[i].Name, want)
		}
	}
	// Label 应是中文。
	if dto.Dimensions[0].Label != "完整性" {
		t.Errorf("Dimensions[0].Label = %q, want 完整性", dto.Dimensions[0].Label)
	}
}

func TestScore_ToDTO_Nil(t *testing.T) {
	var s *Score
	dto := s.ToDTO()
	if dto.Overall != 0 || len(dto.Dimensions) != 0 {
		t.Errorf("nil ToDTO should be zero, got %+v", dto)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
