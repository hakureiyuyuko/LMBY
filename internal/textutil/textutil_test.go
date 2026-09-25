package textutil

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestValid 守住「非法字节不删掉、换成 U+FFFD」：
// 删掉会让两个词粘在一起，看的人不知道这里丢过东西。
func TestValid(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"合法原样返回", "白色相簿 (2009)/S01E16.mkv", "白色相簿 (2009)/S01E16.mkv"},
		{"空串", "", ""},
		{"单个坏字节", "a\xdeb", "a\uFFFDb"},
		{"CIFS 上那种坏名字", "x\xde y.mkv", "x\uFFFD y.mkv"},
		{"截断事故现场（一段非法字节塌成一个替换符）", "\xe3\x80", "\uFFFD"},
		{"合法与非法混在一起", "「完结动画」/\xe3\x80", "「完结动画」/\uFFFD"},
	}
	for _, c := range cases {
		got := Valid(c.in)
		if got != c.want {
			t.Errorf("%s: Valid(%q) = %q，期望 %q", c.name, c.in, got, c.want)
		}
		if !utf8.ValidString(got) {
			t.Errorf("%s: Valid 的结果仍不是合法 UTF-8（%q）", c.name, got)
		}
	}
}

// TestValidKeepsValidAllocationFree 只是把「合法时原样返回」这条写下来：
// 它是热路径（每个路径都要过），靠它避免无意义的内存分配。
func TestValidKeepsValidAllocationFree(t *testing.T) {
	s := "合法字符串"
	if Valid(s) != s || strings.Compare(Valid(s), s) != 0 {
		t.Fatalf("合法输入不应被改动")
	}
}

// TestTruncateNeverSplitsRune 是这次事故的回归测试：
// 旧实现 `s[:n] + "…"` 会把三字节汉字切成 `\xe3\x80`，写进 PG 就是
// `22021 invalid byte sequence for encoding "UTF8"`（日志实证：0xe3 0x80 0xe2）。
func TestTruncateNeverSplitsRune(t *testing.T) {
	s := "白色相簿" // 每个汉字 3 字节，共 12 字节
	for n := 1; n < len(s); n++ {
		got := Truncate(s, n)
		if !utf8.ValidString(got) {
			t.Fatalf("n=%d：截断结果不是合法 UTF-8：%q", n, got)
		}
		if !strings.HasSuffix(got, "…") {
			t.Fatalf("n=%d：真截了就该有省略号，实际 %q", n, got)
		}
		body := strings.TrimSuffix(got, "…")
		if !strings.HasPrefix(s, body) {
			t.Fatalf("n=%d：截断结果不是原串前缀：%q", n, got)
		}
		if len(body) > n {
			t.Fatalf("n=%d：正文部分 %d 字节，超过了上限", n, len(body))
		}
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"不超长原样返回", "abc", 10, "abc"},
		{"正好等于上限", "abc", 3, "abc"},
		{"n<=0 不截断", "abcdef", 0, "abcdef"},
		{"负数不截断", "abcdef", -1, "abcdef"},
		{"ASCII 直接切", "abcdef", 4, "abcd…"},
		{"多字节在边界上回退", "中文", 4, "中…"},  // 4 落在「文」中间 → 退到 3
		{"多字节刚好整数个", "中文", 6, "中文"},   // 没超上限，不截
		{"藏在中间的多字节", "ab中", 4, "ab…"}, // 5 字节 > 4 → 退到 2，因为 3 落在「中」中间
		{"坏字节被净化后截断", "a\xdeb", 3, "a\uFFFDb"},
		{"截断处是坏字节", "ab\xde", 3, "ab\uFFFD"},
	}
	for _, c := range cases {
		got := Truncate(c.in, c.n)
		if got != c.want {
			t.Errorf("%s: Truncate(%q, %d) = %q，期望 %q", c.name, c.in, c.n, got, c.want)
		}
		if !utf8.ValidString(got) {
			t.Errorf("%s: 结果不是合法 UTF-8：%q", c.name, got)
		}
	}
}

// TestTruncateIsAlwaysValid 是「穷举式」的兜底：
// 拿一段刻意构造的脏文本（合法汉字 + 坏字节 + 代理区残缺序列）在所有长度上截一遍，
// 结果必须永远是合法 UTF-8 —— 这正是写进 PG 的硬要求。
func TestTruncateIsAlwaysValid(t *testing.T) {
	dirty := "「完结动画」\xe3\x80\xe2 a\xde b 白色相簿 \xf0\x9f\x8e\xac"
	for n := -2; n <= len(dirty)+2; n++ {
		if got := Truncate(dirty, n); !utf8.ValidString(got) {
			t.Fatalf("n=%d：结果不是合法 UTF-8：%q", n, got)
		}
	}
}

func TestFirstInvalid(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"合法则空", "白色相簿", ""},
		{"空串", "", ""},
		{"单字节", "x\xde y.mkv", "0xde 0x20"},
		{"截断在现场", "\xe3\x80", "0xe3 0x80"},
		{"只报第一段，不把后面的正常字符也倒出来", "好\xde 字\xff 尾", "0xde 0x20"},
		{"坏字节在末尾（后面没字节了）", "名字\xff", "0xff"},
	}
	for _, c := range cases {
		if got := FirstInvalid(c.in); got != c.want {
			t.Errorf("%s: FirstInvalid(%q) = %q，期望 %q", c.name, c.in, got, c.want)
		}
	}
}
