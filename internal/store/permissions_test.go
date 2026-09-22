package store

// 「可见库 → SQL 参数」的规则用表测试钉住。
//
// 为什么这块值得单独测：它在 SQL 层只有**一个**形状（一个 bigint[] 参数，
// cardinality = 0 表示不过滤），却要表达**三种**语义（不过滤 / 只给这几个 /
// 什么都看不到）。一旦映射错了，后果不是「少几行」，而是：
//   · 把 nil 直接当参数 → pgx 绑成 NULL → cardinality(NULL) 是 NULL →
//     where 条件不成立 → **管理员搜什么都 0 条**（真发生过）；
//   · 把「什么都没勾」也映射成空数组 → 与「不过滤」撞车 →
//     白名单开着的人**反而看到全部库**（权限漏洞）。
//
// 所以下面三态逐条钉住，另外把 libraryFilter 生成的表达式也断言一遍
// （它决定了参数怎么被用）。

import (
	"reflect"
	"strings"
	"testing"
)

func TestViewerSQLArgs(t *testing.T) {
	cases := []struct {
		name string
		libs []int64
		want []int64
		why  string
	}{
		{
			name: "nil（管理员 / 没开按库限制）→ 空数组 = 不过滤",
			libs: nil,
			want: []int64{},
			why:  "空数组的 cardinality 是 0，libraryFilter 因此不过滤",
		},
		{
			name: "空白名单（开了限制但一个库都没勾）→ 哨兵 -1 = 什么都看不到",
			libs: []int64{},
			want: []int64{-1},
			why:  "不能也用空数组，否则和「不过滤」撞车，等于把全部库都放开了",
		},
		{
			name: "正常白名单 → 原样",
			libs: []int64{3, 5},
			want: []int64{3, 5},
			why:  "就是要「只放这几个库」",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := Viewer{libs: c.libs}
			if got := v.SQLArgs(); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("SQLArgs() = %v，期望 %v（%s）", got, c.want, c.why)
			}
		})
	}
}

// TestLibraryFilterShape 钉住条件表达式的形状：必须是
// `cardinality($n::bigint[]) = 0 or col = any($n)`。
//
// 单独测它的原因：这个串是所有列表类 SQL 共用的，改它等于改全部读路径的可见性；
// 而它写错时**没有任何编译期信号**（就是一段字符串）。
func TestLibraryFilterShape(t *testing.T) {
	got := libraryFilter("i.library_id", "$7")
	want := "(cardinality($7::bigint[]) = 0 or i.library_id = any($7))"
	if got != want {
		t.Fatalf("libraryFilter() = %q，期望 %q", got, want)
	}
	// 参数占位符必须出现两次（cardinality 与 any 各一次），否则其中一半会失效
	if strings.Count(got, "$7") != 2 {
		t.Fatalf("占位符出现次数不对：%q", got)
	}
}

// TestLibsArgNeverNil 钉住 libsArg 的唯一职责：**绝不返回 nil 切片**。
//
// 返回 nil 会被 pgx 绑成 NULL，而 cardinality(NULL) 是 NULL —— 查询会静默变空。
func TestLibsArgNeverNil(t *testing.T) {
	for _, in := range [][]int64{nil, {}, {1}, {-1}} {
		if got := libsArg(in); got == nil {
			t.Fatalf("libsArg(%v) 返回了 nil —— 会被绑成 NULL，条件失效", in)
		}
	}
	if got := libsArg(nil); len(got) != 0 {
		t.Fatalf("libsArg(nil) 应该是空数组，实际 %v", got)
	}
	// 哨兵要原样透传（它就是「什么都看不到」那个语义）
	if got := libsArg([]int64{-1}); !reflect.DeepEqual(got, []int64{-1}) {
		t.Fatalf("libsArg({-1}) 应该原样返回，实际 %v", got)
	}
}
