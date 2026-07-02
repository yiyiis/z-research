package main

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
// 清洗规则（对齐 gpt-researcher 的 BeautifulSoup 默认 scraper 思路）：
//   - 移除 script/style/nav/header/footer/aside/form 等非正文节点
//   - 优先取 <article>，否则取 <body>
//   - 正文截断到 maxScrapeBytes
//
// 抓取失败（网络/状态码/内容过短）返回错误，调用方据此跳过该 URL。
func FetchURL(ctx context.Context, url string) (*ScrapedPage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

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

	// 移除非正文节点。
	doc.Find("script, style, noscript, nav, header, footer, aside, form, iframe, svg").Remove()

	title := strings.TrimSpace(doc.Find("title").First().Text())

	// 优先 article，否则整个 body。
	sel := doc.Find("article").First()
	if sel.Length() == 0 {
		sel = doc.Find("body")
	}
	content := cleanText(sel.Text())

	if len(content) < 100 {
		return nil, fmt.Errorf("正文过短（%d 字符），可能不是有效内容页", len(content))
	}
	if len(content) > maxScrapeBytes {
		content = content[:maxScrapeBytes]
	}

	return &ScrapedPage{URL: url, Title: title, Content: content}, nil
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
