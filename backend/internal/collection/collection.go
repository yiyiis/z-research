// Package collection 封装研究过程中跨子查询、跨章节、跨递归层共享的
// 来源登记与去重逻辑。
//
// 它提供两类能力：
//
//  1. VisitedSet —— 面试话术里提到的 "visited_urls Set"：一个并发安全的
//     URL→引用编号映射，跨多次 Conduct 调用共享，避免同一 URL 在不同
//     子查询/章节/递归层被重复抓取与重复编号。
//
//  2. Dedup / Merge —— 事后去重与重新编号的工具函数，用于把多批来源
//     合并成一份连续编号 (1..N) 的最终来源列表。
//
// Source 类型是研究中"一条被引用的来源"的统一表示，被 researcher /
// multiagent / deep 三个引擎共享，故放在这里避免循环依赖。
package collection

import (
	"sync"
)

// Source 是一条被引用的来源（带全局编号，用于报告中的 [n] 引用）。
//
// 它是研究中"来源"的统一表示，被 researcher / multiagent / deep 三个
// 引擎共享，故放在 collection 包以避免循环依赖。
// store 包有自己的 Source（结构对齐但独立定义），通过 api 层适配转换。
type Source struct {
	N     int    `json:"n"`     // 引用编号，从 1 开始
	URL   string `json:"url"`   // 来源 URL
	Title string `json:"title"` // 来源标题
}

// VisitedSet 是面试话术里提到的 "visited_urls Set"。
//
// 它记录已访问过的 URL 及其分配的引用编号，跨子查询、跨章节、跨递归层
// 共享。Register 是幂等的：同一 URL 多次登记只会返回首次分配的编号，
// 不会重复加入来源列表，从而避免重复抓取与编号错乱。
//
// 所有方法并发安全。
type VisitedSet struct {
	mu       sync.Mutex
	urls     map[string]int // url → 引用编号（去重）
	sources  []Source       // 按登记顺序的来源列表
	counter  int            // 全局来源编号计数器
}

// NewVisitedSet 创建一个空的 VisitedSet。
func NewVisitedSet() *VisitedSet {
	return &VisitedSet{urls: make(map[string]int)}
}

// Register 登记一个 URL。
//
// 如果 URL 已存在，返回旧编号且不修改状态（幂等）。
// 否则分配一个新的递增编号（从 1 开始），追加到来源列表，并返回新编号。
// 空 URL 会被忽略，返回 0。
func (s *VisitedSet) Register(url, title string) int {
	if url == "" {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.urls[url]; ok {
		return id
	}
	s.counter++
	id := s.counter
	s.urls[url] = id
	s.sources = append(s.sources, Source{N: id, URL: url, Title: title})
	return id
}

// Has 判断 URL 是否已登记。
func (s *VisitedSet) Has(url string) bool {
	if url == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.urls[url]
	return ok
}

// All 返回所有已登记来源的副本（按登记顺序）。
func (s *VisitedSet) All() []Source {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Source, len(s.sources))
	copy(out, s.sources)
	return out
}

// Snapshot 返回所有已登记 URL 的列表（按登记顺序）。用于调试与进度上报。
func (s *VisitedSet) Snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.sources))
	for _, src := range s.sources {
		out = append(out, src.URL)
	}
	return out
}

// Len 返回已登记的来源数量。
func (s *VisitedSet) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sources)
}

// Dedup 按去重后的 URL 顺序返回来源切片，丢弃空 URL。
//
// 用于把一批可能含重复 URL 的来源（例如多个章节各自 Conduct 的产物）
// 去重后合并。保留首次出现的顺序与编号。
func Dedup(in []Source) []Source {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]Source, 0, len(in))
	for _, s := range in {
		if s.URL == "" || seen[s.URL] {
			continue
		}
		seen[s.URL] = true
		out = append(out, s)
	}
	return out
}

// Merge 合并两批来源并重新连续编号 1..N。
//
// existing 通常是一份已去重的来源基线（例如前几章累积的来源），
// newSrcs 是新一批来源。两者按 URL 去重后，重新从 1 开始连续编号，
// 确保最终报告里 [n] 引用与来源列表一一对应。
func Merge(existing []Source, newSrcs []Source) []Source {
	seen := make(map[string]bool, len(existing)+len(newSrcs))
	out := make([]Source, 0, len(existing)+len(newSrcs))
	for _, s := range existing {
		if s.URL != "" && !seen[s.URL] {
			seen[s.URL] = true
			out = append(out, s)
		}
	}
	for _, s := range newSrcs {
		if s.URL != "" && !seen[s.URL] {
			seen[s.URL] = true
			out = append(out, s)
		}
	}
	// 重新连续编号 1..N
	for i := range out {
		out[i].N = i + 1
	}
	return out
}
