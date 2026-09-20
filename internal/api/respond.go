package api

import (
	"encoding/json"
	"net"
	"net/http"
)

// errorBody 是所有失败响应的统一形状。
type errorBody struct {
	Error string `json:"error"`
}

// writeJSON 输出 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// 响应头已发出，只能记录；调用方通常也不关心。
		_ = err
	}
}

// writeError 输出统一格式的错误响应。
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorBody{Error: message})
}

// decodeJSON 解析请求体（限 1 MiB，拒绝未知字段以便尽早暴露接口误用）。
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "请求体格式错误："+err.Error())
		return false
	}
	return true
}

// clientIP 取客户端地址。
//
// 注意：M0 只信任 TCP 对端地址，不解析 X-Forwarded-For。
// 将来若支持反代部署，需要引入「可信代理」白名单再启用 XFF。
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
