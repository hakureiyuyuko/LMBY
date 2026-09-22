package livetv

import (
	"crypto/hmac"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// 外链播放 token：给外部播放器（VLC / PotPlayer / 手机播放器）用的**无状态**凭据。
//
// 为什么不落库：token 只是「这个频道 + 到什么时候为止」这两件事，
// 服务端用 HMAC 签名就能自证真伪 —— 不存表就不用清理、重启也不丢，
// 而 M7 要做「限次数/限下载」时再引入带状态的分享表（那时才真需要存）。
//
// 格式：`<频道id>-<过期unix秒>-<hmac 十六进制前 16 字节>`（全是路径安全字符）。
const tokenDomain = "lmby/livetv/play-token/v1"

var (
	// ErrTokenInvalid 表示 token 格式不对或签名对不上（被改过/密钥换了）。
	ErrTokenInvalid = errors.New("播放链接无效")
	// ErrTokenExpired 表示链接过期了。
	ErrTokenExpired = errors.New("播放链接已过期")
)

// Signer 是签名需要的最小接口（secrets.Cipher 实现了它）。
//
// 定成接口而不是直接依赖 secrets 包：单测里可以塞一个假签名器，
// 也避免 livetv 为了签名去拿整个密钥文件的依赖。
type Signer interface {
	Sign(domain string, msg []byte) []byte
}

// SignPlayToken 生成一个到 expires 为止有效的播放 token。
func SignPlayToken(s Signer, channelID int64, expires time.Time) string {
	payload := fmt.Sprintf("%d-%d", channelID, expires.Unix())
	sum := s.Sign(tokenDomain, []byte(payload))
	return payload + "-" + hex.EncodeToString(sum[:16])
}

// ParsePlayToken 校验 token 并返回频道 id。
//
// 校验三件事：格式、签名（常数时间比较）、过期。
func ParsePlayToken(s Signer, token string, now time.Time) (int64, error) {
	parts := strings.Split(token, "-")
	if len(parts) != 3 {
		return 0, ErrTokenInvalid
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		return 0, ErrTokenInvalid
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, ErrTokenInvalid
	}
	want := hex.EncodeToString(s.Sign(tokenDomain, []byte(parts[0]+"-"+parts[1]))[:16])
	if !hmac.Equal([]byte(want), []byte(parts[2])) {
		return 0, ErrTokenInvalid
	}
	if now.Unix() > exp {
		return 0, ErrTokenExpired
	}
	return id, nil
}
