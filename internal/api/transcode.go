package api

import (
	"net/http"
	"strings"

	"github.com/hakureiyuyuko/lmby/internal/encoder"
	"github.com/hakureiyuyuko/lmby/internal/playback"
)

// machineForDecision 把本机能力表压成决策层需要的那几个事实。
//
// 决策层（internal/playback）故意不认识 internal/encoder —— 它只关心
// 「能不能编 h264/hevc、能不能硬解、能不能做色调映射」，不关心是 VAAPI 还是 QSV。
// 这一层做转换，顺便保证「没探测出能力」时退化成保守值（不能转码），
// 而不是默认能转。
func machineForDecision(caps *encoder.Capabilities, backend encoder.Backend) playback.Machine {
	m := playback.Machine{Name: backend.Name, Hardware: backend.Kind != encoder.KindSoftware}
	for _, codec := range []string{"h264", "hevc"} {
		if backend.Encode[codec] {
			m.EncodeCodecs = append(m.EncodeCodecs, codec)
		}
		if backend.Decode[codec] {
			m.DecodeHW = append(m.DecodeHW, codec)
		}
	}
	// 色调映射的判据是「软件链（zscale + tonemap）在不在」，与后端无关。
	//
	// 曾经按「后端有没有 tonemap_vaapi / tonemap_opencl」判，但实测证明那是假阳性：
	// iHD 的 tonemap_vaapi 要求输入带 mastering display 元数据，而库里常见的
	// bt2020+PQ 素材没有那段数据，滤镜会直接报错、整路转码起不来。
	// 现在硬编路径也走软件色调映射（见 encoder.vaapiVideoArgs），
	// 所以能力表只需要回答「这两个软件滤镜在不在」。
	m.Tonemap = hasFilterAll(caps, "zscale", "tonemap")
	return m
}

func hasFilterAll(caps *encoder.Capabilities, names ...string) bool {
	for _, want := range names {
		found := false
		for _, f := range caps.Filters {
			if strings.EqualFold(f, want) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// handleTranscodeCapabilities 输出本机的编码能力表。
//
// 登录用户都能看：排查「为什么这片子卡」时，第一件事就是看这台机器到底能用哪个后端。
// 里面的每一条都是**真跑过**的结论（不是「ffmpeg 有这个编码器」），
// 所以它能直接回答「我这台机器到底能不能硬解 HEVC」。
func (s *Server) handleTranscodeCapabilities(w http.ResponseWriter, r *http.Request) {
	caps, err := s.encoders.Get(r.Context())
	if err != nil {
		s.serverError(w, "编码能力探测失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"capabilities": caps,
		"best":         s.encoders.Preferred(caps),
	})
}

// handleRefreshTranscodeCapabilities 强制重新探测（管理员）。
//
// 什么时候要用：装了显卡驱动、换了机器、升级 ffmpeg 之后 ——
// 这些变化在 24 小时的缓存有效期内不会被发现。
func (s *Server) handleRefreshTranscodeCapabilities(w http.ResponseWriter, r *http.Request) {
	caps, err := s.encoders.Refresh(r.Context())
	if err != nil {
		s.serverError(w, "重新探测编码能力失败", err)
		return
	}
	s.log.Info("已手动重新探测编码能力", "首选", s.encoders.Preferred(caps).Name, "耗时ms", caps.ElapsedMS)
	writeJSON(w, http.StatusOK, map[string]any{"capabilities": caps, "best": s.encoders.Preferred(caps)})
}
