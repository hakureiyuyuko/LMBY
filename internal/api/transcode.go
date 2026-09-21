package api

import (
	"net/http"
)

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
