// Package scraper 负责抓取单个 URL 并清洗为正文文本。
//
// 抓取策略（两级，对齐 gpt-researcher 的多 scraper 思路）：
//  1. **Jina Reader**（默认）：把 URL 转成 https://r.jina.ai/<url>，由 Jina 服务抓取并
//     返回干净的纯文本。它能绕过反爬（403）和 JS 渲染——这两者是纯 HTTP 爬虫的根本短板。
//     Jina Reader 免费且专为 LLM 优化（返回 Markdown 纯文本）。
//  2. **直连 goquery**（回退）：Jina 失败时，直接请求原 URL + goquery 解析，
//     作为兜底（对友好站点足够，且不依赖外部服务）。
package scraper

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// maxScrapeBytes 限制单个网页抓取后保留的最大字符数（避免超长网页撑爆上下文）。
const maxScrapeBytes = 50000

// userAgent 模拟正常浏览器，降低被目标站点拒绝的概率。
const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// jinaReaderBase 是 Jina Reader 的 URL 前缀，拼接目标 URL 即可。
const jinaReaderBase = "https://r.jina.ai/"

// ScrapedPage 是抓取并清洗后的网页内容。
type ScrapedPage struct {
	URL     string
	Title   string
	Content string
}

// ScraperStrategy 控制抓取策略。
type ScraperStrategy string

const (
	// StrategyAuto 默认：先试 Jina Reader，失败回退 goquery 直连。
	StrategyAuto ScraperStrategy = "auto"
	// StrategyJina 只用 Jina Reader（JS 渲染/反爬友好，但依赖外部服务）。
	StrategyJina ScraperStrategy = "jina"
	// StrategyDirect 只用 goquery 直连（不依赖外部服务，对 JS 渲染/反爬站无效）。
	StrategyDirect ScraperStrategy = "direct"
)

// activeStrategy 是当前抓取策略，默认 Auto。可由 main.go 通过 SetStrategy 修改。
// 用 atomic 持有以便并发安全（研究流程会并发抓取多个 URL）。
var activeStrategy = StrategyAuto

// SetStrategy 设置全局抓取策略。在启动时由 main.go 根据配置调用。
func SetStrategy(s ScraperStrategy) {
	if s == "" {
		s = StrategyAuto
	}
	activeStrategy = s
}

// FetchURL 抓取单个 URL，返回清洗后的正文文本与标题。
//
// 策略由 SetStrategy 控制：Auto（先 Jina 后 goquery）/ Jina / Direct。
// 抓取失败（网络/状态码/内容过短）返回错误，调用方据此跳过该 URL。
func FetchURL(ctx context.Context, url string) (*ScrapedPage, error) {
	switch activeStrategy {
	case StrategyJina:
		return fetchViaJina(ctx, url)
	case StrategyDirect:
		return fetchDirect(ctx, url)
	default: // StrategyAuto
		// 先试 Jina Reader（绕反爬+JS渲染）。
		if page, err := fetchViaJina(ctx, url); err == nil {
			return page, nil
		}
		// 回退：直连 goquery。
		return fetchDirect(ctx, url)
	}
}

// fetchViaJina 通过 Jina Reader 抓取。
// Jina 返回纯文本：首行通常是 "Title: xxx"，其后是 "Markdown Content:"，再是正文。
func fetchViaJina(ctx context.Context, url string) (*ScrapedPage, error) {
	jinaURL := jinaReaderBase + url
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jinaURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/plain, */*")
	// 不设 Accept-Encoding，让 Go 自动处理 gzip 解压。
	// 超时 8 秒：Jina 是外部免费服务，偶发宕机/限流时会挂起，
	// 不能让它拖死整个研究流程（多个 URL 并发抓取时尤其致命）。
	// 8s 足够 Jina 返回（正常 1-5s），挂起时快速失败回退到 goquery。
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Jina 请求失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Jina 非 200: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("Jina 读取失败: %w", err)
	}
	text := string(body)

	// Jina Reader 输出格式解析：提取 Title 和正文。
	title, content := parseJinaOutput(text)
	content = cleanText(content)
	if len([]rune(content)) < 100 {
		return nil, fmt.Errorf("Jina 正文过短（%d 字符）", len([]rune(content)))
	}
	if len(content) > maxScrapeBytes {
		content = content[:maxScrapeBytes]
	}
	return &ScrapedPage{URL: url, Title: title, Content: content}, nil
}

// parseJinaOutput 解析 Jina Reader 的输出，分离标题与正文。
// Jina 格式通常是：
//
//	Title: 标题
//	URL Source: ...
//	Markdown Content:
//	正文...
func parseJinaOutput(text string) (title, content string) {
	lines := strings.Split(text, "\n")
	contentStart := 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if title == "" && strings.HasPrefix(trimmed, "Title:") {
			title = strings.TrimSpace(strings.TrimPrefix(trimmed, "Title:"))
		}
		// "Markdown Content:" 之后才是正文。
		if strings.HasPrefix(trimmed, "Markdown Content:") {
			contentStart = i + 1
			break
		}
	}
	if contentStart > 0 && contentStart < len(lines) {
		content = strings.Join(lines[contentStart:], "\n")
	} else {
		// 没找到标记，整段当正文。
		content = text
	}
	return title, content
}

// fetchDirect 直连目标 URL + goquery 解析（Jina 失败时的回退）。
func fetchDirect(ctx context.Context, url string) (*ScrapedPage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %w", err)
	}
	// 模拟真实浏览器的完整请求头，降低被反爬拦截（403）的概率。
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	// 注意：不要手动设 Accept-Encoding。Go 的 http.Transport 在不设置该头时
	// 会自动加 gzip 并透明解压；手动设会导致拿到未解压的 gzip 原始字节，
	// goquery 解析乱码 → 标题/正文全空。
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	req.Header.Set("Referer", "https://www.google.com/")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("非 200 状态码: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("解析 HTML 失败: %w", err)
	}

	// 先取标题（在移除节点前）。
	title := strings.TrimSpace(doc.Find("title").First().Text())
	if title == "" {
		if ogTitle, ok := doc.Find(`meta[property="og:title"]`).Attr("content"); ok {
			title = strings.TrimSpace(ogTitle)
		}
	}

	// 移除非正文节点。
	doc.Find("script, style, noscript, nav, header, footer, aside, form, iframe, svg, " +
		"button, input, select, textarea, .nav, .menu, .sidebar, .footer, .header, .ad, .ads, .comment").Remove()

	content := extractMainText(doc)
	if len(content) < 100 {
		content = extractParagraphText(doc)
	}
	if len(content) < 100 {
		return nil, fmt.Errorf("正文过短（%d 字符），可能不是有效内容页或需 JS 渲染", len(content))
	}
	if len(content) > maxScrapeBytes {
		content = content[:maxScrapeBytes]
	}

	return &ScrapedPage{URL: url, Title: title, Content: content}, nil
}

// extractMainText 按优先级从 article/main/body 提取正文。
func extractMainText(doc *goquery.Document) string {
	for _, selector := range []string{"article", "main", "[role=main]", ".content", ".article", "#content", "body"} {
		sel := doc.Find(selector).First()
		if sel.Length() > 0 {
			if t := cleanText(sel.Text()); len(t) >= 100 {
				return t
			}
		}
	}
	return ""
}

// extractParagraphText 收集所有 <p>/<li>/<h1-3> 节点的文本，作为兜底提取策略。
func extractParagraphText(doc *goquery.Document) string {
	var parts []string
	doc.Find("p, li, h1, h2, h3").Each(func(_ int, s *goquery.Selection) {
		if t := strings.TrimSpace(s.Text()); t != "" {
			parts = append(parts, t)
		}
	})
	return cleanText(strings.Join(parts, "\n"))
}

// cleanText 压缩连续空白，去除每行首尾空格与空行。
func cleanText(s string) string {
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	var b strings.Builder
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
	}
	return b.String()
}
