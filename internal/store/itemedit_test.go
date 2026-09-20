package store

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// 这些测试全是离线的：拼 SQL 与值换算不该只能靠在真库上试。
// （SQL 真正跑起来的效果由 scripts/dev/verify-item-edit.sh 在真库上验。）

func TestItemEditStatementKinds(t *testing.T) {
	sql, args, err := itemEditStatement(7, map[string]any{
		"title":        "黑客帝国",
		"year":         int64(1999),
		"runtime":      int64(136), // 分钟
		"rating":       8.7,
		"genres":       []string{"科幻", "动作"},
		"providerIds":  map[string]string{"tmdb": "603"},
		"premiereDate": "1999-03-31",
	}, nil, "")
	if err != nil {
		t.Fatalf("itemEditStatement: %v", err)
	}

	want := []any{
		"黑客帝国",                       // title
		int32(1999),                  // year（列是 integer）
		int64(136) * 60 * 10_000_000, // runtime 分钟 → tick
		8.7,                          // rating
		`["科幻","动作"]`,                // genres（jsonb 文本）
		`{"tmdb":"603"}`,             // providerIds（jsonb 文本）
		time.Date(1999, 3, 31, 0, 0, 0, 0, time.UTC), // premiereDate
		"黑客帝国",   // sort_title：从上面的 title 派生（《The Matrix》那类开头的冠词会去掉）
		int64(7), // where id
	}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("参数不符：\n实际 %#v\n期望 %#v", args, want)
	}

	for _, frag := range []string{
		"title = $1",
		"year = $2",
		"runtime_ticks = $3",
		"community_rating = $4",
		"genres = $5::jsonb",
		"provider_ids = $6::jsonb",
		"premiere_date = $7",
		"sort_title = $8",
		"metadata_source = 'manual'",
		"updated_at = now()",
		"where id = $9 and deleted_at is null",
	} {
		if !strings.Contains(sql, frag) {
			t.Errorf("SQL 里缺少 %q：\n%s", frag, sql)
		}
	}
}

func TestItemEditStatementClears(t *testing.T) {
	// null（Go 的 nil）表示清空：可空列落 NULL，文本落空串，数组/对象落空结构。
	sql, args, err := itemEditStatement(3, map[string]any{
		"year":   nil,
		"rating": nil,
		"genres": []string{},
	}, nil, "")
	if err != nil {
		t.Fatalf("itemEditStatement: %v", err)
	}
	want := []any{nil, nil, "[]", int64(3)}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("参数不符：实际 %#v 期望 %#v", args, want)
	}
	if !strings.Contains(sql, "genres = $3::jsonb") {
		t.Errorf("空数组也要真的写进去（人工编辑是显式表达）：\n%s", sql)
	}
}

func TestItemEditStatementUnknownField(t *testing.T) {
	// 打错字段名必须报错（"providers" 是真实踩过的写法）
	_, _, err := itemEditStatement(1, map[string]any{"providers": "x"}, nil, "")
	if !errors.Is(err, ErrUnknownItemField) {
		t.Fatalf("未知字段应当返回 ErrUnknownItemField，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "providerIds") {
		t.Errorf("错误信息里应当列出可用字段名：%v", err)
	}

	if _, _, err := itemEditStatement(1, nil, &[]string{"nope"}, ""); !errors.Is(err, ErrUnknownItemField) {
		t.Errorf("锁定集合里的未知字段也应当报错，实际 %v", err)
	}
}

func TestItemEditStatementLockedFields(t *testing.T) {
	sql, args, err := itemEditStatement(9, nil, &[]string{"year", "title"}, "")
	if err != nil {
		t.Fatalf("itemEditStatement: %v", err)
	}
	// 排过序，读日志与对比时稳定
	if got := args[0]; got != `["title","year"]` {
		t.Errorf("锁定集合 = %v，期望 [\"title\",\"year\"]", got)
	}
	if !strings.Contains(sql, "locked_fields = $1::jsonb") {
		t.Errorf("SQL 不含锁定集合写入：\n%s", sql)
	}
	// 只改锁定集合时不该动 metadata_source
	if strings.Contains(sql, "metadata_source") {
		t.Errorf("仅改锁定时不该改元数据来源：\n%s", sql)
	}

	// 空切片 = 全部解锁（与 nil「不改」区分开）
	_, args, err = itemEditStatement(9, nil, &[]string{}, "")
	if err != nil {
		t.Fatalf("itemEditStatement: %v", err)
	}
	if args[0] != "[]" {
		t.Errorf("空锁定集合应当写成 []，实际 %v", args[0])
	}
}

func TestItemEditStatementNothingToDo(t *testing.T) {
	if _, _, err := itemEditStatement(1, nil, nil, ""); err == nil {
		t.Error("什么都没给时应当报错，而不是发一条空的 UPDATE")
	}
	// 只有 matchState 也算有活干
	if _, _, err := itemEditStatement(1, nil, nil, MatchStateManual); err != nil {
		t.Errorf("只改状态也应当允许：%v", err)
	}
}

func TestItemEditStatementValueErrors(t *testing.T) {
	cases := []struct {
		name   string
		values map[string]any
	}{
		{"文本字段给了整数", map[string]any{"title": 42}},
		{"年份超范围", map[string]any{"year": int64(999999)}},
		{"时长是负数", map[string]any{"runtime": int64(-1)}},
		{"评分超出 0~10", map[string]any{"rating": 95.0}},
		{"打分给了字符串", map[string]any{"genres": "科幻"}},
		{"日期格式不对", map[string]any{"premiereDate": "1999/03/31"}},
	}
	for _, c := range cases {
		if _, _, err := itemEditStatement(1, c.values, nil, ""); err == nil {
			t.Errorf("%s 应当报错", c.name)
		}
	}
}

// TestApplyItemMetaGuardsEveryEditableField 是这次改动最关键的不变量：
// 表里每一个可编辑（可锁定）字段，都必须在 ApplyItemMeta 的 SQL 里有锁判断。
//
// 少了任何一个，「界面上锁住了、重扫还是被覆盖」就会静默发生 ——
// 这类错误在数据里看不出来，只能靠这条测试挡住。
func TestApplyItemMetaGuardsEveryEditableField(t *testing.T) {
	for _, f := range ItemFields() {
		guard := "locked_fields ? '" + f.Name + "'"
		if !strings.Contains(applyItemMetaSQL, guard) {
			t.Errorf("字段 %s（列 %s）在 ApplyItemMeta 里没有锁判断 %q", f.Name, f.Column, guard)
		}
	}
}

func TestItemFieldsLookup(t *testing.T) {
	f, ok := LookupItemField("providerIds")
	if !ok || f.Column != "provider_ids" || f.Kind != ItemFieldMap {
		t.Errorf("providerIds 查表结果不对: %+v ok=%v", f, ok)
	}
	if _, ok := LookupItemField("Providers"); ok {
		t.Error("字段名区分大小写，不该匹配 Providers")
	}
	if _, ok := LookupItemField(""); ok {
		t.Error("空字段名不该匹配")
	}
	// 返回的是副本：调用方改不动全局表
	fields := ItemFields()
	fields[0].Name = "hacked"
	if again, _ := LookupItemField(fields[0].Name); again.Name == "hacked" {
		t.Error("ItemFields 应当返回副本")
	}
}

func TestFieldLocked(t *testing.T) {
	locked := []string{"title", "year"}
	if !FieldLocked(locked, "title") || FieldLocked(locked, "overview") {
		t.Error("FieldLocked 判断不对")
	}
	if FieldLocked(nil, "title") {
		t.Error("空锁定集合应当一律返回 false")
	}
}

func TestSortTitle(t *testing.T) {
	cases := map[string]string{
		"The Matrix":      "matrix",
		"A Beautiful Day": "beautiful day",
		"  言叶之庭  ":        "言叶之庭",
		"- 风筝":            "风筝",
		"":                "",
	}
	for in, want := range cases {
		if got := SortTitle(in); got != want {
			t.Errorf("SortTitle(%q) = %q，期望 %q", in, got, want)
		}
	}
}
