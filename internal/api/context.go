package api

import (
	"context"
	"net/http"
	"time"
)

// contextWithTimeout 在请求 context 上再叠一层超时。
func contextWithTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}
