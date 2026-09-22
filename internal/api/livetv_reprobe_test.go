package api

// 「起播失败 → 要不要自动重探」的规则用表测试钉住。
//
// 为什么值得单独测：这条规则一旦松掉，最坏的后果不是「多探一次」，而是
// **把一台健康的频道标成失效**（前台从此不显示它，管理员只能自己再手动探测
// 一次才能恢复）。所以本地资源问题必须排除、节流必须生效，两条都要有测试。

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/hakureiyuyuko/lmby/internal/stream"
)

func TestReprobeAfterPlayFailure(t *testing.T) {
	now := time.Date(2026, 9, 22, 20, 0, 0, 0, time.UTC)
	timeoutErr := errors.New("等待转封装起步超时：等待第一个分片超过 8s")

	cases := []struct {
		name    string
		err     error
		last    time.Time
		hasLast bool
		want    bool
		why     string
	}{
		{
			name: "源站拉不起来 → 探",
			err:  timeoutErr,
			want: true,
			why:  "这就是这条逻辑存在的理由：源站可能变了，真探一次拿到最新答案",
		},
		{
			name: "没有失败 → 不探",
			err:  nil,
			want: false,
			why:  "起播成功时不该顺手去连源站（那是白花的连接）",
		},
		{
			name: "本地并发打满 → 不探",
			err:  stream.ErrTooMany,
			want: false,
			why:  "本地资源问题跟源站没关系，探了会把好频道标成失效",
		},
		{
			name: "并发打满被包装过 → 也不探",
			err:  fmt.Errorf("起播失败：%w", stream.ErrTooMany),
			want: false,
			why:  "错误是层层包上来的，必须用 errors.Is 判，不能比字符串",
		},
		{
			name:    "刚探过（30 秒前）→ 不探",
			err:     timeoutErr,
			last:    now.Add(-30 * time.Second),
			hasLast: true,
			want:    false,
			why:     "观众反复点、多人同时点同一台，只该探一次",
		},
		{
			name:    "节流窗口刚过（61 秒）→ 探",
			err:     timeoutErr,
			last:    now.Add(-61 * time.Second),
			hasLast: true,
			want:    true,
			why:     "源站恢复得比节流窗口慢的时候，还得再探一次",
		},
		{
			name:    "刚好到窗口边界（60 秒）→ 探",
			err:     timeoutErr,
			last:    now.Add(-liveReprobeMinInterval),
			hasLast: true,
			want:    true,
			why:     "判的是「小于窗口才跳过」，等于窗口时放行（避免边界上卡住不探）",
		},
		{
			name:    "有记录但时间在未来（时钟回拨）→ 不探",
			err:     timeoutErr,
			last:    now.Add(5 * time.Minute),
			hasLast: true,
			want:    false,
			why:     "now.Sub(last) 为负 → 落在窗口内，不会因为时钟回拨变成「每次失败都探」",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := reprobeAfterPlayFailure(c.err, c.last, c.hasLast, now)
			if got != c.want {
				t.Fatalf("reprobeAfterPlayFailure() = %v，期望 %v（%s）", got, c.want, c.why)
			}
		})
	}
}
