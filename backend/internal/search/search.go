// Package search 封装网络搜索引擎，当前实现为 DuckDuckGo 文本搜索。
//
// 使用 eino-ext duckduckgo/v2 的 NewSearch 直接调用接口（结构化返回，非 Agent 路径），
// 返回统一的 SearchResult，屏蔽底层 DuckDuckGo 返回类型的差异。
package search

import (
	"context"
	"fmt"
	"strings"
	"time"

	duckduckgo "github.com/cloudwego/eino-ext/components/tool/duckduckgo/v2"
)

// SearchResult 是统一的搜索结果结构。
type SearchResult struct {
	Title   string
	URL     string
	Snippet string
}

// Searcher 封装 DuckDuckGo 文本搜索。
type Searcher struct {
	search duckduckgo.Search
}

// NewSearcher 创建一个 DuckDuckGo 搜索器。
// 默认全球范围 (RegionWT)，单次请求 15s 超时。
//
// 网络连通性（如 DDG 是否可达）由运行环境负责：可在系统层配置 HTTP 代理、
// 使用 TUN 模式，或部署在境外服务器。应用代码不内置代理逻辑。
func NewSearcher(ctx context.Context) (*Searcher, error) {
	s, err := duckduckgo.NewSearch(ctx, &duckduckgo.Config{
		MaxResults: 10, // 上限，实际返回数由调用方 max 控制
		Region:     duckduckgo.RegionWT,
		Timeout:    15 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("创建 DuckDuckGo 搜索器失败: %w", err)
	}
	return &Searcher{search: s}, nil
}

// Search 用 query 进行文本搜索，最多返回 max 条结果。
// 返回的结果已过滤掉标题/URL/摘要全空的项。
func (s *Searcher) Search(ctx context.Context, query string, max int) ([]SearchResult, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}

	resp, err := s.search.TextSearch(ctx, &duckduckgo.TextSearchRequest{Query: query})
	if err != nil {
		return nil, fmt.Errorf("DuckDuckGo 搜索 %q 失败: %w", query, err)
	}

	results := make([]SearchResult, 0, len(resp.Results))
	for _, r := range resp.Results {
		if r == nil {
			continue
		}
		if strings.TrimSpace(r.Title) == "" && strings.TrimSpace(r.Summary) == "" && strings.TrimSpace(r.URL) == "" {
			continue
		}
		results = append(results, SearchResult{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: r.Summary,
		})
		if max > 0 && len(results) >= max {
			break
		}
	}
	return results, nil
}
