package scraper

import (
	"context"
	"testing"
	"time"
)

// TestFetchURL_Real 抓取真实 URL，验证 FetchURL 是否能正常工作。
// 这些 URL 是研究流程里 DuckDuckGo 实际返回的。
func TestFetchURL_Real(t *testing.T) {
	urls := []string{
		"https://www.woshipm.com/marketing/6159848.html",
		"https://zhuanlan.zhihu.com/p/24314799491",
		"https://www.chinapp.com/best/liansuojiudian.html",
	}
	ctx := context.Background()
	for _, u := range urls {
		ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
		page, err := FetchURL(ctx, u)
		cancel()
		if err != nil {
			t.Logf("❌ %s -> 错误: %v", u, err)
			continue
		}
		t.Logf("✅ %s -> %d 字符, 标题=%q", u, len(page.Content), page.Title)
	}
}
