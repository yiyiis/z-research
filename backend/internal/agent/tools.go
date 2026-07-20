// Package agent 实现 ReAct Agent 引擎——让 LLM 自主决定调用搜索/抓取工具、
// 何时停止，最后输出报告。这是 z-research 的第三种引擎模式（与 single/multi 并列）。
//
// 与 single（确定性工作流）的区别：Agent 模式下 LLM 控制循环（ReAct）、
// 选择工具、判定终止，是真正的 autonomous agent。
//
// 工具错误处理策略：工具失败时**不返回 error 给 Agent**（那会让 Agent 整个流程崩），
// 而是返回结构化的错误 JSON（{"error":..., "hint":...}），让 LLM 看懂后调整策略
// （换关键词、换 URL、或决定资料已够直接写报告）。这是 ReAct 范式的关键实践。
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"z-research/backend/internal/scraper"
	"z-research/backend/internal/search"
)

// SearchToolInput 是 web_search 工具的输入参数。
type SearchToolInput struct {
	Query      string `json:"query" jsonschema:"description=搜索查询词（Google 风格关键词）"`
	MaxResults int    `json:"max_results,omitempty" jsonschema:"description=返回结果数上限（默认5），minimum=1,maximum=10"`
}

// searchOutput 是 web_search 工具的输出（JSON 字符串）。
type searchOutput struct {
	Results []search.SearchResult `json:"results"`
	// Error 字段：工具失败时填，LLM 据此调整策略。成功时为空。
	Error string `json:"error,omitempty"`
	Hint  string `json:"hint,omitempty"`
}

// toolErrorJSON 构造一个错误 JSON 字符串返回给 LLM。
// 这是"友好错误"——LLM 看到 error 字段会理解工具失败，并从 hint 调整策略。
func toolErrorJSON(msg, hint string) string {
	out := searchOutput{Error: msg, Hint: hint}
	b, _ := json.Marshal(out)
	return string(b)
}

// NewSearchTool 把 DuckDuckGo 搜索封装成一个 InvokableTool。
// LLM 可通过 tool_call 调用它搜索网页。
//
// 失败处理：返回错误 JSON（而非 error），让 LLM 决定换关键词或换策略。
func NewSearchTool(searcher *search.Searcher) (tool.InvokableTool, error) {
	if searcher == nil {
		return nil, fmt.Errorf("searcher 不能为空")
	}
	return utils.InferTool(
		"web_search",
		"在互联网上搜索信息，返回相关网页的标题、URL 和摘要。用于查找研究资料。当需要了解某个主题时调用。",
		func(ctx context.Context, in *SearchToolInput) (string, error) {
			q := strings.TrimSpace(in.Query)
			if q == "" {
				// 参数错误：返回友好错误，不 return err。
				return toolErrorJSON("查询词不能为空", "请提供非空的搜索关键词"), nil
			}
			max := in.MaxResults
			if max <= 0 || max > 10 {
				max = 5
			}
			results, err := searcher.Search(ctx, q, max)
			if err != nil {
				// 搜索失败：返回错误 JSON，hint 告诉 LLM 可以换词或继续用已有资料。
				return toolErrorJSON(
					fmt.Sprintf("搜索失败: %v", err),
					"可尝试换一组关键词，或基于已收集的资料直接撰写报告",
				), nil
			}
			if len(results) == 0 {
				return toolErrorJSON("搜索无结果", "可尝试换一组关键词"), nil
			}
			out := searchOutput{Results: results}
			b, err := json.Marshal(out)
			if err != nil {
				return toolErrorJSON(fmt.Sprintf("结果序列化失败: %v", err), ""), nil
			}
			return string(b), nil
		},
	)
}

// FetchToolInput 是 fetch_url 工具的输入参数。
type FetchToolInput struct {
	URL string `json:"url" jsonschema:"description=要抓取的网页URL"`
}

// FetchToolResult 是抓取结果。
type FetchToolResult struct {
	Title   string `json:"title"`
	Content string `json:"content"`
	// Error 字段：工具失败时填。
	Error string `json:"error,omitempty"`
	Hint  string `json:"hint,omitempty"`
}

// fetchErrorJSON 构造 fetch 工具的错误 JSON。
func fetchErrorJSON(msg, hint string) string {
	out := FetchToolResult{Error: msg, Hint: hint}
	b, _ := json.Marshal(out)
	return string(b)
}

// NewFetchTool 把网页抓取（Jina + goquery 回退）封装成 InvokableTool。
// LLM 可通过 tool_call 调用它读取某个 URL 的正文内容。
//
// 失败处理：
//   - 参数错误：返回友好错误 JSON（不 return err）
//   - 抓取失败：单次重试（很多网站首次 403 二次能通），仍失败则返回错误 JSON
//     让 LLM 换 URL 或放弃该网页。
func NewFetchTool() (tool.InvokableTool, error) {
	return utils.InferTool(
		"fetch_url",
		"抓取指定URL的网页正文内容（自动清洗，去除导航/广告等噪音）。用于读取搜索结果中某个网页的详细内容。当需要深入了解某个网页时调用。",
		func(ctx context.Context, in *FetchToolInput) (string, error) {
			url := strings.TrimSpace(in.URL)
			if url == "" {
				return fetchErrorJSON("url 不能为空", "请提供有效的网页 URL"), nil
			}
			// 首次抓取。
			page, err := scraper.FetchURL(ctx, url)
			if err == nil {
				out := FetchToolResult{Title: page.Title, Content: page.Content}
				b, _ := json.Marshal(out)
				return string(b), nil
			}
			// 首次失败：重试一次（瞬时故障/403 常见，重试常成功）。
			page, err2 := scraper.FetchURL(ctx, url)
			if err2 == nil {
				out := FetchToolResult{
					Title:   page.Title,
					Content: page.Content,
					// 标注首次失败但重试成功，便于调试（LLM 可忽略）。
					Hint:    "(首次抓取失败，重试成功)",
				}
				b, _ := json.Marshal(out)
				return string(b), nil
			}
			// 两次都失败：返回友好错误，让 LLM 换 URL 或放弃。
			return fetchErrorJSON(
				fmt.Sprintf("抓取失败（已重试）: %v", err),
				"该网页可能无法访问或被反爬拦截，建议尝试其他来源 URL",
			), nil
		},
	)
}
