// Package agent 实现 ReAct Agent 引擎——让 LLM 自主决定调用搜索/抓取工具、
// 何时停止，最后输出报告。这是 z-research 的第三种引擎模式（与 single/multi 并列）。
//
// 与 single（确定性工作流）的区别：Agent 模式下 LLM 控制循环（ReAct）、
// 选择工具、判定终止，是真正的 autonomous agent。
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
}

// NewSearchTool 把 DuckDuckGo 搜索封装成一个 InvokableTool。
// LLM 可通过 tool_call 调用它搜索网页。
func NewSearchTool(searcher *search.Searcher) (tool.InvokableTool, error) {
	if searcher == nil {
		return nil, fmt.Errorf("searcher 不能为空")
	}
	return utils.InferTool(
		"web_search",
		"在互联网上搜索信息，返回相关网页的标题、URL 和摘要。用于查找研究资料。当需要了解某个主题时调用。",
		func(ctx context.Context, in *SearchToolInput) (string, error) {
			max := in.MaxResults
			if max <= 0 || max > 10 {
				max = 5
			}
			results, err := searcher.Search(ctx, in.Query, max)
			if err != nil {
				return "", fmt.Errorf("搜索失败: %w", err)
			}
			out := searchOutput{Results: results}
			b, err := json.Marshal(out)
			if err != nil {
				return "", err
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
}

// NewFetchTool 把网页抓取（Jina + goquery 回退）封装成 InvokableTool。
// LLM 可通过 tool_call 调用它读取某个 URL 的正文内容。
func NewFetchTool() (tool.InvokableTool, error) {
	return utils.InferTool(
		"fetch_url",
		"抓取指定URL的网页正文内容（自动清洗，去除导航/广告等噪音）。用于读取搜索结果中某个网页的详细内容。当需要深入了解某个网页时调用。",
		func(ctx context.Context, in *FetchToolInput) (string, error) {
			url := strings.TrimSpace(in.URL)
			if url == "" {
				return "", fmt.Errorf("url 不能为空")
			}
			page, err := scraper.FetchURL(ctx, url)
			if err != nil {
				return "", fmt.Errorf("抓取失败: %w", err)
			}
			out := FetchToolResult{Title: page.Title, Content: page.Content}
			b, err := json.Marshal(out)
			if err != nil {
				return "", err
			}
			return string(b), nil
		},
	)
}
