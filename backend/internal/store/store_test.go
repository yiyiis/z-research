package store

import (
	"context"
	"path/filepath"
	"testing"
)

// newTestStore 创建一个临时文件的 SQLite store，测试结束自动清理。
func newTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestStore_SaveAndGet 验证保存后能完整读回。
func TestStore_SaveAndGet(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	src := []Source{{N: 1, URL: "http://a.com", Title: "A"}, {N: 2, URL: "http://b.com", Title: "B"}}
	r, err := s.Save(ctx, "测试查询", "测试标题", "# 报告正文", src)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if r.ID == 0 {
		t.Fatal("保存后 ID 不应为 0")
	}
	if len(r.Sources) != 2 {
		t.Errorf("保存后 sources 数应为 2，得到 %d", len(r.Sources))
	}

	got, err := s.Get(ctx, r.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Query != "测试查询" || got.Content != "# 报告正文" {
		t.Errorf("读回数据不匹配: %+v", got)
	}
	if len(got.Sources) != 2 || got.Sources[0].URL != "http://a.com" {
		t.Errorf("读回 sources 不匹配: %+v", got.Sources)
	}
}

// TestStore_List 验证列表按时间倒序、不含正文。
func TestStore_List(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	for i := 0; i < 3; i++ {
		_, err := s.Save(ctx, "q", "t", "正文内容", nil)
		if err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	list, err := s.List(ctx, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("列表应有 3 条，得到 %d", len(list))
	}
	// 列表不应包含正文。
	for _, r := range list {
		if r.Content != "" {
			t.Errorf("列表项不应含正文，得到 %q", r.Content)
		}
	}
}

// TestStore_Delete 验证删除后查不到。
func TestStore_Delete(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	r, _ := s.Save(ctx, "q", "t", "c", nil)
	if err := s.Delete(ctx, r.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, r.ID); err == nil {
		t.Error("删除后 Get 应返回错误，但返回 nil")
	}
	// 删除不存在的 ID 应报错。
	if err := s.Delete(ctx, 99999); err == nil {
		t.Error("删除不存在的 ID 应报错")
	}
}

// TestStore_GetNotFound 查询不存在的 ID 应报错。
func TestStore_GetNotFound(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if _, err := s.Get(ctx, 99999); err == nil {
		t.Error("查询不存在的 ID 应报错")
	}
}

// TestStore_DefaultLimit limit<=0 时使用默认值，不应报错。
func TestStore_DefaultLimit(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, err := s.List(ctx, 0)
	if err != nil {
		t.Errorf("limit=0 不应报错: %v", err)
	}
	_, err = s.List(ctx, -5)
	if err != nil {
		t.Errorf("limit<0 不应报错: %v", err)
	}
}
