// Package scraper 负责抓取单个 URL 并清洗为正文文本（去除导航/脚本等噪音）。
//
// 清洗规则（对齐 gpt-researcher 的 BeautifulSoup 默认 scraper 思路）：
//   - 移除 script/style/nav/header/footer/aside/form 等非正文节点
//   - 优先取 <article>，否则 <main>，再否则整个 <body>
//   - 若仍取不到正文，回退为收集所有 <p> 节点文本（应对部分动态站点）
//   - 正文截断到 maxScrapeBytes
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

// ScrapedPage 是抓取并清洗后的网页内容。
type ScrapedPage struct {
	URL     string
	Title   string
	Content string
}

// FetchURL 抓取单个 URL，返回清洗后的正文文本与标题。
//
// 抓取失败（网络/状态码/内容过短）返回错误，调用方据此跳过该 URL。
func FetchURL(ctx context.Context, url string) (*ScrapedPage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %w", err)
	}
	// 模拟真实浏览器的完整请求头，降低被反爬拦截（403）的概率。
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Accept-Encoding", "gzip, deflate")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Upgrade-Insecure-Requests", "1")

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

	// 先取标题（在移除节点前，避免 <title> 受影响）。
	title := strings.TrimSpace(doc.Find("title").First().Text())
	// 部分站点用 og:title 元数据，作为补充。
	if title == "" {
		if ogTitle, ok := doc.Find(`meta[property="og:title"]`).Attr("content"); ok {
			title = strings.TrimSpace(ogTitle)
		}
	}

	// 移除非正文节点。
	doc.Find("script, style, noscript, nav, header, footer, aside, form, iframe, svg, " +
		"button, input, select, textarea, .nav, .menu, .sidebar, .footer, .header, .ad, .ads, .comment").Remove()

	// 按优先级尝试多个容器：article → main → body。
	content := extractMainText(doc)

	// 兜底：若以上都取不到正文，收集所有 <p>/<li> 节点（应对正文不在标准容器里的站点）。
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
