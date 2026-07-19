package researcher

import (
	"testing"

	"z-research/backend/internal/collection"
)

// TestAppendUnique 验证子查询去重逻辑。
func TestAppendUnique(t *testing.T) {
	list := []string{"a", "b"}
	list = appendUnique(list, "a") // 已存在，应跳过
	if len(list) != 2 {
		t.Errorf("重复项应被跳过，得到 %v", list)
	}
	list = appendUnique(list, "c") // 新项，应追加
	if len(list) != 3 || list[2] != "c" {
		t.Errorf("新项应追加，得到 %v", list)
	}
	list = appendUnique(list, "  ") // 空项应跳过
	if len(list) != 3 {
		t.Errorf("空项应跳过，得到 %v", list)
	}
}

// TestVisitedSet_Integration 验证 researcher 通过 collection.VisitedSet
// 完成来源登记（替代了原本私有的 collector 类型）。
// 完整的 VisitedSet 行为测试见 internal/collection/collection_test.go。
func TestVisitedSet_Integration(t *testing.T) {
	v := collection.NewVisitedSet()
	id1 := v.Register("http://a.com", "A")
	if id1 != 1 {
		t.Errorf("首个来源编号应为 1，得到 %d", id1)
	}
	id2 := v.Register("http://b.com", "B")
	if id2 != 2 {
		t.Errorf("第二个来源编号应为 2，得到 %d", id2)
	}
	// 重复 URL 应返回旧编号，不新增。
	if id := v.Register("http://a.com", "A again"); id != 1 {
		t.Errorf("重复 URL 应返回旧编号 1，得到 %d", id)
	}
	all := v.All()
	if len(all) != 2 {
		t.Errorf("应有 2 个来源（去重后），得到 %d", len(all))
	}
	// 验证记录的是首次登记的标题（last-write-wins 不应用于首次登记场景）。
	if all[0].Title != "A" {
		t.Errorf("重复登记不应覆盖标题，得到 %q", all[0].Title)
	}
}
