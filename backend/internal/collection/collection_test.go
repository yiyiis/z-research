package collection

import (
	"sync"
	"testing"
)

func TestVisitedSet_RegisterIsIdempotent(t *testing.T) {
	s := NewVisitedSet()
	id1 := s.Register("https://example.com/a", "A")
	id2 := s.Register("https://example.com/b", "B")
	id3 := s.Register("https://example.com/a", "A dup") // 重复 URL

	if id1 != 1 || id2 != 2 {
		t.Errorf("first two ids = %d,%d; want 1,2", id1, id2)
	}
	if id3 != id1 {
		t.Errorf("duplicate Register id = %d, want %d (old)", id3, id1)
	}
	if got := s.Len(); got != 2 {
		t.Errorf("Len = %d, want 2 (dedup)", got)
	}
	if !s.Has("https://example.com/a") || s.Has("https://nope") {
		t.Errorf("Has mismatch")
	}
}

func TestVisitedSet_EmptyURLIgnored(t *testing.T) {
	s := NewVisitedSet()
	if id := s.Register("", "empty"); id != 0 {
		t.Errorf("Register('') id = %d, want 0", id)
	}
	if s.Len() != 0 {
		t.Errorf("Len = %d, want 0", s.Len())
	}
}

func TestVisitedSet_ConcurrentRegister(t *testing.T) {
	s := NewVisitedSet()
	const goroutines = 20
	const perG = 50
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				// 故意让不同 goroutine 竞争同一批 URL（g*perG + i 唯一，
				// 但 i 部分有重叠）。
				s.Register("https://example.com/"+itoa(g%3)+"-"+itoa(i), "x")
			}
		}(g)
	}
	wg.Wait()
	// 唯一 URL 数 = 3 个 g 组 × perG = 150
	if got := s.Len(); got != 3*perG {
		t.Errorf("Len = %d, want %d", got, 3*perG)
	}
	// 编号应连续唯一 1..N
	all := s.All()
	seen := make(map[int]bool, len(all))
	for _, src := range all {
		if src.N < 1 || src.N > len(all) {
			t.Errorf("id %d out of range [1,%d]", src.N, len(all))
		}
		if seen[src.N] {
			t.Errorf("duplicate id %d", src.N)
		}
		seen[src.N] = true
	}
}

func TestDedup(t *testing.T) {
	in := []Source{
		{N: 1, URL: "https://a", Title: "A"},
		{N: 2, URL: "https://b", Title: "B"},
		{N: 3, URL: "https://a", Title: "A dup"}, // 重复
		{N: 4, URL: "", Title: "empty"},         // 空 URL 丢弃
	}
	out := Dedup(in)
	if len(out) != 2 {
		t.Fatalf("Dedup len = %d, want 2: %+v", len(out), out)
	}
	if out[0].URL != "https://a" || out[1].URL != "https://b" {
		t.Errorf("Dedup order wrong: %+v", out)
	}
}

func TestMerge_Renumbers(t *testing.T) {
	existing := []Source{
		{N: 5, URL: "https://a"},
		{N: 9, URL: "https://b"},
	}
	newSrcs := []Source{
		{N: 1, URL: "https://b"}, // 重复，应跳过
		{N: 2, URL: "https://c"},
	}
	out := Merge(existing, newSrcs)
	if len(out) != 3 {
		t.Fatalf("Merge len = %d, want 3: %+v", len(out), out)
	}
	for i, s := range out {
		if s.N != i+1 {
			t.Errorf("Merge renumber: out[%d].N = %d, want %d", i, s.N, i+1)
		}
	}
	wantURLs := []string{"https://a", "https://b", "https://c"}
	for i, want := range wantURLs {
		if out[i].URL != want {
			t.Errorf("Merge order: out[%d].URL = %q, want %q", i, out[i].URL, want)
		}
	}
}

// itoa 简易整数转字符串，避免引入 strconv 使测试更轻。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	if n < 0 {
		b = append(b, '-')
		n = -n
	}
	var tmp []byte
	for n > 0 {
		tmp = append(tmp, byte('0'+n%10))
		n /= 10
	}
	for i := len(tmp) - 1; i >= 0; i-- {
		b = append(b, tmp[i])
	}
	return string(b)
}
