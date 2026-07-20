// Package eval 实现研究报告的 LLM-as-Judge 自动评估。
//
// 对齐 RAGAS 的核心思路:用一个 LLM 当评委,按多维度 rubric 给报告打分,
// 输出结构化分数 + 扣分原因。评估在报告生成完成后异步触发,结果持久化
// 到 report_evaluations 表(关联 reports.id),前端在历史列表/报告详情展示。
//
// 评估维度(5 个,可配置裁剪):
//   - coverage    完整性:是否覆盖查询的核心方面
//   - citation    引用质量:[n] 引用是否充分、对应真实来源
//   - structure   结构组织:章节合理、逻辑递进
//   - objectivity 客观性:避免主观臆断、基于资料
//   - readability 可读性:语言流畅、术语一致
//
// 关键设计:
//   - 评委只看"报告 + 来源列表",不引入自己的知识(避免与被评模型同源偏见)
//   - 失败不致命:评估出错时返回 nil + error,调用方降级为"不展示评分"
//   - 评估是只读的,不改报告本身
package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"z-research/backend/internal/llm"
)

// SourceRow 是评委需要的来源信息(与 researcher.Source 对齐,但 eval 包不依赖 researcher)。
// 避免 import 循环:researcher 已经不依赖 eval,但保持解耦更干净。
type SourceRow struct {
	N     int    `json:"n"`
	URL   string `json:"url"`
	Title string `json:"title"`
}

// DimensionScore 是单个维度的评分 + 扣分原因。
type DimensionScore struct {
	Score float64 `json:"score"` // 0-10
	Note  string  `json:"note"`  // 扣分原因(中文)
}

// Score 是一次评估的完整结果。
type Score struct {
	// Dimensions 是各维度的评分。key 是维度名(coverage/citation/structure/objectivity/readability)。
	Dimensions map[string]DimensionScore `json:"dimensions"`
	// Summary 是一句话总评(中文,50字以内)。
	Summary string `json:"summary"`
}

// Overall 返回综合分(各维度平均,0-10)。
// 用平均而非加权——不同场景对各维度重视不同,平均最中性,
// 前端若要加权可基于 Dimensions 自行计算。
func (s *Score) Overall() float64 {
	if s == nil || len(s.Dimensions) == 0 {
		return 0
	}
	var sum float64
	for _, d := range s.Dimensions {
		sum += d.Score
	}
	return sum / float64(len(s.Dimensions))
}

// JudgeReport 用 LLM-as-Judge 给一篇报告打分。
//
// 参数:
//   - query: 原始研究查询(评委据此判断 coverage)
//   - report: 报告 Markdown 正文
//   - sources: 报告声明的来源列表(评委据此核对 [n] 引用真实性)
//
// 用 smart 档位(评委需要理解全文,fast 档位理解力不足)。
// 内部走 chatJSONWith,自带 JSON 修复重试(思考模型截断时也能拿到结果)。
//
// 失败时返回 (nil, error),调用方应降级(不展示评分),不要让评估失败影响主流程。
func JudgeReport(ctx context.Context, l *llm.LLM, query, report string, sources []SourceRow) (*Score, error) {
	if l == nil {
		return nil, fmt.Errorf("eval: LLM 为空")
	}
	if strings.TrimSpace(report) == "" {
		return nil, fmt.Errorf("eval: 报告为空")
	}

	// 用 ChatJSONRole 拿结构化输出,role="judge" 便于 usage 按角色聚合。
	var raw struct {
		Dimensions map[string]struct {
			Score float64 `json:"score"`
			Note  string  `json:"note"`
		} `json:"dimensions"`
		Summary string `json:"summary"`
	}
	if err := l.ChatJSONRole(ctx, "judge", JudgeSystemPrompt,
		JudgeUserPrompt(query, report, sources), &raw); err != nil {
		return nil, fmt.Errorf("eval: 评委 LLM 调用失败: %w", err)
	}

	// 规范化:维度名小写、分数 clamp 到 [0,10]、缺失维度补零。
	score := &Score{
		Dimensions: make(map[string]DimensionScore, len(raw.Dimensions)),
		Summary:    strings.TrimSpace(raw.Summary),
	}
	for name, d := range raw.Dimensions {
		s := d.Score
		if s < 0 {
			s = 0
		}
		if s > 10 {
			s = 10
		}
		score.Dimensions[strings.ToLower(strings.TrimSpace(name))] = DimensionScore{
			Score: s,
			Note:  strings.TrimSpace(d.Note),
		}
	}
	// 若关键维度缺失,补零(让前端展示完整,而非缺项)。
	for _, dim := range defaultDimensions {
		if _, ok := score.Dimensions[dim]; !ok {
			score.Dimensions[dim] = DimensionScore{Score: 0, Note: "评委未给出此维度评分"}
		}
	}
	if len(score.Dimensions) == 0 {
		return nil, fmt.Errorf("eval: 评委返回空 dimensions")
	}
	return score, nil
}

// defaultDimensions 是默认评估的 5 个维度(与 JudgeSystemPrompt 的 rubric 对齐)。
// 用于:1) 缺失维度补零;2) 配置裁剪时校验。
var defaultDimensions = []string{"coverage", "citation", "structure", "objectivity", "readability"}

// ScoreDTO 是给 API 层/前端的传输结构(扁平化,便于 JSON 序列化和前端渲染)。
//
// Score 本身的 Dimensions 是 map(适合按名查),但前端展示雷达图/进度条
// 需要有序列表,故提供 DTO 转换。
type ScoreDTO struct {
	Overall    float64            `json:"overall"`     // 综合分(平均)
	Summary    string             `json:"summary"`     // 一句话总评
	Dimensions []DimensionScoreDTO `json:"dimensions"` // 有序维度列表
}

// DimensionScoreDTO 是单个维度的 DTO(带名字)。
type DimensionScoreDTO struct {
	Name  string  `json:"name"`  // coverage/citation/...
	Label string  `json:"label"` // 中文标签(完整性/引用质量/...)
	Score float64 `json:"score"` // 0-10
	Note  string  `json:"note"`  // 扣分原因
}

// dimensionLabels 是维度的中文标签(前端展示用)。
var dimensionLabels = map[string]string{
	"coverage":    "完整性",
	"citation":    "引用质量",
	"structure":   "结构组织",
	"objectivity": "客观性",
	"readability": "可读性",
}

// Label 返回维度的中文标签(未知维度返回原名)。
func Label(dim string) string {
	if l, ok := dimensionLabels[dim]; ok {
		return l
	}
	return dim
}

// ToDTO 把 Score 转成 ScoreDTO(维度按 defaultDimensions 顺序排列)。
func (s *Score) ToDTO() ScoreDTO {
	if s == nil {
		return ScoreDTO{}
	}
	dto := ScoreDTO{
		Overall: s.Overall(),
		Summary: s.Summary,
	}
	// 按 defaultDimensions 顺序输出,保证前端渲染稳定。
	for _, dim := range defaultDimensions {
		if d, ok := s.Dimensions[dim]; ok {
			dto.Dimensions = append(dto.Dimensions, DimensionScoreDTO{
				Name:  dim,
				Label: Label(dim),
				Score: d.Score,
				Note:  d.Note,
			})
		}
	}
	// 补上非默认维度(若评委返回了额外维度)。
	for name, d := range s.Dimensions {
		known := false
		for _, dim := range defaultDimensions {
			if dim == name {
				known = true
				break
			}
		}
		if !known {
			dto.Dimensions = append(dto.Dimensions, DimensionScoreDTO{
				Name: name, Label: Label(name), Score: d.Score, Note: d.Note,
			})
		}
	}
	return dto
}

// MarshalJSON 让 ScoreDTO 能被 json.Marshal 直接序列化(已实现结构体 tag)。
// 这个方法只是为了避免 unused warning(实际序列化由默认反射机制处理)。
func (d ScoreDTO) MarshalJSON() ([]byte, error) {
	type alias ScoreDTO
	return json.Marshal(alias(d))
}

// Ptr 返回 d 的指针(便于 wsMessage 的 Evaluation *ScoreDTO 字段赋值)。
func (d ScoreDTO) Ptr() *ScoreDTO { return &d }
