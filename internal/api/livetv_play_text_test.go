package api

// 「起播失败 → 人话」的映射表测试。
//
// 为什么要有它：这段唯一的职责就是「别让实现细节漏到界面上」，而
// `errors.Is` 与「原始串」很容易退化成「把 err.Error() 拼进文案」——
// 那样界面又会出现「等待转封装起步超时」这种词。另外并发打满与源站挂了
// **必须分成两个 code**：前者用户自己能解决（停一路），后者只能等/换台。

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/hakureiyuyuko/lmby/internal/stream"
)

func TestLivePlayFailText(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode string
		// wantParts 是文案里必须出现的子串；wantNot 是绝不该出现的（实现细节）。
		wantParts []string
		wantNot   []string
	}{
		{
			name:      "并发打满（本地资源）",
			err:       stream.ErrTooMany,
			wantCode:  liveErrBusy,
			wantParts: []string{"上限", "停掉一路"},
			wantNot:   []string{"转封装", "分片", "8s"},
		},
		{
			name:      "并发打满（被包装过）",
			err:       fmt.Errorf("起播失败：%w", stream.ErrTooMany),
			wantCode:  liveErrBusy,
			wantParts: []string{"上限"},
		},
		{
			name: "等第一个分片超时（真实现场那句）",
			err: fmt.Errorf("%w：%w%s", stream.ErrStartTimeout,
				errors.New("等待第一个分片超过 8s"), "\nffmpeg 的最后几行"),
			wantCode: liveErrSourceTimeout,
			// 源站这条要说清「可能失效」+「已经自动重探过」——用户才知道不用再狂点
			wantParts: []string{"源站", "可能已失效", "自动重新探测"},
			wantNot:   []string{"转封装", "分片", "等待第一个分片"},
		},
		{
			name:      "其他拉流失败：兜底带上原始原因（至少能贴给管理员）",
			err:       errors.New("Invalid data found when processing input"),
			wantCode:  liveErrFailed,
			wantParts: []string{"拉流失败", "Invalid data found"},
		},
		{
			name:     "没有错误时什么都不给",
			err:      nil,
			wantCode: "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, text := livePlayFailText(c.err)
			if code != c.wantCode {
				t.Fatalf("code = %q，期望 %q", code, c.wantCode)
			}
			for _, p := range c.wantParts {
				if !strings.Contains(text, p) {
					t.Errorf("文案里应该出现 %q，实际：%q", p, text)
				}
			}
			for _, p := range c.wantNot {
				if strings.Contains(text, p) {
					t.Errorf("文案里不该出现实现细节 %q，实际：%q", p, text)
				}
			}
		})
	}
}
