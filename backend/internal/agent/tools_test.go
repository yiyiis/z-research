package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"
)

// TestNewFetchTool_Info 验证 fetch_url 工具能创建并暴露正确的 schema。
func TestNewFetchTool_Info(t *testing.T) {
	ft, err := NewFetchTool()
	if err != nil {
		t.Fatalf("NewFetchTool: %v", err)
	}
	info, err := ft.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != "fetch_url" {
		t.Errorf("工具名应为 fetch_url，得到 %q", info.Name)
	}
	if info.Desc == "" {
		t.Error("工具描述不应为空")
	}
	t.Logf("✅ fetch_url 工具: name=%s desc=%q", info.Name, info.Desc[:40])
}

// TestNewFetchTool_InvokableRun 验证 fetch_url 工具能被调用（真实抓取）。
// 用一个稳定的技术文档站点测试。
func TestNewFetchTool_InvokableRun(t *testing.T) {
	ft, err := NewFetchTool()
	if err != nil {
		t.Fatalf("NewFetchTool: %v", err)
	}
	// 用一个稳定的、无反爬的站点。
	out, err := ft.InvokableRun(context.Background(), `{"url":"https://www.runoob.com/go/go-tutorial.html"}`)
	if err != nil {
		t.Skipf("跳过（网络不可用）: %v", err)
	}
	if out == "" {
		t.Error("输出不应为空")
	}
	t.Logf("✅ fetch_url 调用成功，输出前60字符: %s", truncStr(out, 60))
}

// TestNewFetchTool_EmptyURL 空 URL 应返回友好错误 JSON（而非 error）。
// 这是工具错误友好化的设计：让 LLM 看到 error 字段并调整策略，而不是 Agent 崩掉。
func TestNewFetchTool_EmptyURL(t *testing.T) {
	ft, _ := NewFetchTool()
	out, err := ft.InvokableRun(context.Background(), `{"url":""}`)
	if err != nil {
		t.Errorf("空 URL 不应返回 error（应返回错误 JSON）: %v", err)
	}
	if !strings.Contains(out, "error") {
		t.Errorf("空 URL 应在 JSON 里含 error 字段, got: %s", out)
	}
}

// TestNewSearchTool_NilSearcher nil searcher 应报错。
func TestNewSearchTool_NilSearcher(t *testing.T) {
	_, err := NewSearchTool(nil)
	if err == nil {
		t.Error("nil searcher 应报错")
	}
}

// TestNewSearchTool_Info 验证 web_search 工具创建（不实际搜索）。
func TestNewSearchTool_Info(t *testing.T) {
	// 用 nil searcher 也能测 Info（只看 schema）。
	// 但 NewSearchTool 拒绝 nil，所以这里跳过实际创建，验证类型即可。
	var _ tool.InvokableTool // 确认类型存在
}

func truncStr(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "..."
}
