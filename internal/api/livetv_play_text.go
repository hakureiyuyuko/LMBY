package api

// 直播起播失败的「人话 + 可翻译标识」。
//
// 为什么不能把 err 原样抛给界面：里面全是实现细节，用户看不懂也不知道该做什么。
// 以前用户在直播页看到的是：
//
//	拉流失败：等待转封装起步超时：等待第一个分片超过 8s
//
// 「转封装」「起步」「分片」是服务端的词。用户真正需要知道的是三件事：
// **发生了什么、我们已经做了什么、你现在该做什么**。所以这里把它翻成一句话，
// 同时给一个稳定的 code —— 界面（非中文时）拿 code 自己查 i18n 词条，
// 后端这句中文只作兜底（与 i18n.ts 头部的设计第 4 条一致）。
//
// 原始 err 仍然原样进日志（`s.log.Warn("直播起播失败", …)`），排查不受影响。

import (
	"errors"

	"github.com/hakureiyuyuko/lmby/internal/stream"
)

const (
	// liveErrBusy：本地并发打满。**与源站无关**，用户自己能解决（停掉一路）。
	// 单独一个 code 是因为它的话术与「源站挂了」完全不同，不能混。
	liveErrBusy = "livetv_busy"
	// liveErrSourceTimeout：等到第一个分片超时（源站不应答 / 推不出流）。
	liveErrSourceTimeout = "livetv_source_timeout"
	// liveErrFailed：其他拉流失败（参数拼装、源站直接拒…），兜底。
	liveErrFailed = "livetv_failed"
)

// livePlayFailText 把起播错误翻成 (code, 给用户看的一句话)。
//
// err 为 nil 时返回空（调用方本来就只在失败时问）。
func livePlayFailText(err error) (string, string) {
	switch {
	case err == nil:
		return "", ""
	case errors.Is(err, stream.ErrTooMany):
		return liveErrBusy, "同时播放的路数已达上限，先停掉一路再试"
	case errors.Is(err, stream.ErrStartTimeout):
		// 措辞刻意带上「已自动重新探测」：起播失败时后台真的会重探一次
		// （见 livetv_reprobe.go），前台因此会自己把失效的台收起来。
		return liveErrSourceTimeout, "源站没有及时返回画面（这个频道可能已失效，我们已经自动重新探测过）"
	}
	return liveErrFailed, "拉流失败：" + err.Error()
}
