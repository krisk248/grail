package http

import (
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

func chain(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

func recoverMW(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rv := recover(); rv != nil {
					log.Error("panic in handler", "url", r.URL.Path, "panic", rv, "stack", string(debug.Stack()))
					http.Error(w, "internal server error", http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

type statusRW struct {
	http.ResponseWriter
	code int
}

func (s *statusRW) WriteHeader(c int) { s.code = c; s.ResponseWriter.WriteHeader(c) }

func loggerMW(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			srw := &statusRW{ResponseWriter: w, code: 200}
			next.ServeHTTP(srw, r)
			log.Info("http", "method", r.Method, "path", r.URL.Path, "status", srw.code, "ms", time.Since(start).Milliseconds())
		})
	}
}

// loginLimiter rate-limits POSTs to /admin/login by client IP. 5/min, burst 5.
type loginLimiter struct {
	mu   sync.Mutex
	bins map[string]*rate.Limiter
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{bins: map[string]*rate.Limiter{}}
}

func (l *loginLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	lim, ok := l.bins[ip]
	if !ok {
		lim = rate.NewLimiter(rate.Every(12*time.Second), 5)
		l.bins[ip] = lim
	}
	return lim.Allow()
}

func clientIP(r *http.Request) string {
	if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
		parts := strings.Split(xf, ",")
		return strings.TrimSpace(parts[0])
	}
	if xr := r.Header.Get("X-Real-IP"); xr != "" {
		return xr
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
