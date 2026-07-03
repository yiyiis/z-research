package researcher

import "testing"

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

// TestCollector_RegisterSource 验证来源去重与编号递增。
func TestCollector_RegisterSource(t *testing.T) {
	c := &collector{urlToSourceID: make(map[string]int)}

	id1 := c.registerSource("http://a.com", "A")
	if id1 != 1 {
		t.Errorf("首个来源编号应为 1，得到 %d", id1)
	}
	id2 := c.registerSource("http://b.com", "B")
	if id2 != 2 {
		t.Errorf("第二个来源编号应为 2，得到 %d", id2)
	}
	// 重复 URL 应返回旧编号，不新增。
	id1Again := c.registerSource("http://a.com", "A again")
	if id1Again != 1 {
		t.Errorf("重复 URL 应返回旧编号 1，得到 %d", id1Again)
	}
	if len(c.sources) != 2 {
		t.Errorf("应有 2 个来源（去重后），得到 %d", len(c.sources))
	}
	// 验证记录的是首次登记的标题。
	if c.sources[0].Title != "A" {
		t.Errorf("重复登记不应覆盖标题，得到 %q", c.sources[0].Title)
	}
}
