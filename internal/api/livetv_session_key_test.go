package api

// 会话键 → 频道 id 的解析用表测试钉住。
//
// 这段曾经错过一次，而且**错得非常安静**：直接对 `live:ch<id>` 做 TrimPrefix 再
// ParseInt，遇到带处理方式后缀的键（`live:ch29:copy`）就解析失败，而失败分支是
// `continue` —— 于是管理面「会话」页里的直播列表**永远空着**，没有任何报错，
// 直到发版前的回归才被逮住（验证脚本断言「两观众共一路」的会话数）。
//
// 所以这里把「带后缀 / 不带后缀 / 不是直播键 / 垃圾键」都列出来。

import "testing"

func TestLiveChannelIDFromKey(t *testing.T) {
	cases := []struct {
		key    string
		wantID int64
		wantOK bool
	}{
		// 现在的形态：键带处理方式后缀
		{"live:ch29:copy", 29, true},
		{"live:ch29:transcode", 29, true},
		{"live:ch111:copy", 111, true},
		// 早期形态：没有后缀（旧会话/旧数据也要能认）
		{"live:ch7", 7, true},
		// 不是直播键：点播转封装（vod:…）与其它键都要被跳过
		{"vod:12-remux", 0, false},
		{"live:", 0, false},
		{"live:ch", 0, false},
		// 垃圾键：解析失败就跳过（不能让管理接口 500）
		{"live:chabc:copy", 0, false},
		{"live:ch-3:copy", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		id, ok := liveChannelIDFromKey(c.key)
		if id != c.wantID || ok != c.wantOK {
			t.Errorf("liveChannelIDFromKey(%q) = (%d, %v)，期望 (%d, %v)", c.key, id, ok, c.wantID, c.wantOK)
		}
	}
}
