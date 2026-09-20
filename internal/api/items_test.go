package api

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/hakureiyuyuko/lmby/internal/store"
)

func rawFields(t *testing.T, body string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("测试数据不是合法 JSON: %v", err)
	}
	return m
}

func TestDecodeItemFieldsByKind(t *testing.T) {
	got, err := decodeItemFields(rawFields(t, `{
		"title": "黑客帝国",
		"year": 1999,
		"runtime": 136,
		"rating": 8.7,
		"genres": ["科幻", "动作"],
		"studios": [],
		"providerIds": {"tmdb": "603"},
		"premiereDate": "1999-03-31"
	}`))
	if err != nil {
		t.Fatalf("decodeItemFields: %v", err)
	}
	want := map[string]any{
		"title":        "黑客帝国",
		"year":         int64(1999),
		"runtime":      int64(136), // 分钟；换算成 tick 是 store 的事
		"rating":       8.7,
		"genres":       []string{"科幻", "动作"},
		"studios":      []string{},
		"providerIds":  map[string]string{"tmdb": "603"},
		"premiereDate": "1999-03-31",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("解析结果不符：\n实际 %#v\n期望 %#v", got, want)
	}
}

func TestDecodeItemFieldsNullClears(t *testing.T) {
	got, err := decodeItemFields(rawFields(t, `{
		"title": "",
		"year": null,
		"runtime": null,
		"rating": null,
		"genres": null,
		"providerIds": null,
		"premiereDate": null
	}`))
	if err != nil {
		t.Fatalf("decodeItemFields: %v", err)
	}
	want := map[string]any{
		"title":        "",
		"year":         nil,
		"runtime":      nil,
		"rating":       nil,
		"genres":       []string{},
		"providerIds":  map[string]string{},
		"premiereDate": nil,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("清空语义不符：\n实际 %#v\n期望 %#v", got, want)
	}
}

func TestDecodeItemFieldsUnknownField(t *testing.T) {
	// "providers" 是真实踩过的写法：静默忽略会让人以为改生效了
	_, err := decodeItemFields(rawFields(t, `{"providers": "603"}`))
	if !errors.Is(err, store.ErrUnknownItemField) {
		t.Fatalf("未知字段应当报 ErrUnknownItemField，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "providerIds") {
		t.Errorf("错误信息应当列出可用字段名：%v", err)
	}
}

func TestDecodeItemFieldsTypeMismatch(t *testing.T) {
	cases := []string{
		`{"year": "1999"}`,
		`{"runtime": "两小时"}`,
		`{"rating": "很高"}`,
		`{"genres": "科幻"}`,
		`{"providerIds": ["tmdb"]}`,
		`{"premiereDate": 19990331}`,
		`{"title": 42}`,
	}
	for _, body := range cases {
		if _, err := decodeItemFields(rawFields(t, body)); err == nil {
			t.Errorf("%s 类型不对，应当报错", body)
		}
	}
}

func TestDecodeItemFieldsEmpty(t *testing.T) {
	if got, err := decodeItemFields(nil); got != nil || err != nil {
		t.Errorf("空 fields 应当是 nil + 无错误，实际 %v %v", got, err)
	}
}
