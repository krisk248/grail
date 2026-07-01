package http

import (
	"crypto/sha256"
	"encoding/base64"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
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
			// 5xx→Error, 4xx→Warn, state-changing→Info, routine 2xx GETs→Debug
			// (so the every-10s /api/state polling and static assets stay quiet
			// unless LOG_LEVEL=debug).
			lvl := slog.LevelInfo
			switch {
			case srw.code >= 500:
				lvl = slog.LevelError
			case srw.code >= 400:
				lvl = slog.LevelWarn
			case r.Method == http.MethodGet || r.Method == http.MethodHead:
				lvl = slog.LevelDebug
			}
			log.LogAttrs(r.Context(), lvl, "http",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", srw.code),
				slog.Int64("ms", time.Since(start).Milliseconds()))
		})
	}
}

// securityHeadersMW sets defensive response headers. csp is the fully-built
// Content-Security-Policy; hsts enables Strict-Transport-Security (TLS only).
func securityHeadersMW(csp string, hsts bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
			h.Set("Content-Security-Policy", csp)
			if hsts {
				h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			}
			next.ServeHTTP(w, r)
		})
	}
}

// buildCSP assembles the policy. scriptExtra is an extra script/connect origin
// (e.g. the Umami host); scriptHashes are 'sha256-...' tokens for the specific
// inline scripts we ship (SvelteKit bootstrap + gate page), so those run under
// a strict policy without opening 'unsafe-inline'.
func buildCSP(scriptExtra string, scriptHashes []string) string {
	scriptSrc := "'self'"
	connectSrc := "'self'"
	if scriptExtra != "" {
		scriptSrc += " " + scriptExtra
		connectSrc += " " + scriptExtra
	}
	for _, h := range scriptHashes {
		scriptSrc += " " + h
	}
	return strings.Join([]string{
		"default-src 'self'",
		"base-uri 'self'",
		"frame-ancestors 'none'",
		"form-action 'self'",
		"img-src 'self' data:",
		"style-src 'self' 'unsafe-inline'",
		"script-src " + scriptSrc,
		"connect-src " + connectSrc,
	}, "; ")
}

// cspSourceFromURL extracts a "scheme://host[:port]" origin from a full URL,
// suitable for use as a CSP source. Returns "" if the URL can't be parsed.
func cspSourceFromURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

var inlineScriptRE = regexp.MustCompile(`(?is)<script([^>]*)>(.*?)</script>`)

// inlineScriptHashes returns 'sha256-<b64>' CSP tokens for every inline
// (no src=) <script> in the given HTML — the exact value a browser computes to
// authorize that inline script under a hash-based CSP.
func inlineScriptHashes(html string) []string {
	var out []string
	for _, m := range inlineScriptRE.FindAllStringSubmatch(html, -1) {
		if strings.Contains(strings.ToLower(m[1]), "src=") {
			continue
		}
		sum := sha256.Sum256([]byte(m[2]))
		out = append(out, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
	}
	return out
}

// loginLimiter rate-limits POSTs to /admin/login by client IP. 5/min, burst 5.
type loginLimiter struct {
	mu   sync.Mutex
	bins map[string]*limiterEntry
}

type limiterEntry struct {
	lim  *rate.Limiter
	seen time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{bins: map[string]*limiterEntry{}}
}

func (l *loginLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.evict(now)
	e, ok := l.bins[ip]
	if !ok {
		e = &limiterEntry{lim: rate.NewLimiter(rate.Every(12*time.Second), 5)}
		l.bins[ip] = e
	}
	e.seen = now
	return e.lim.Allow()
}

// evict drops entries untouched for over an hour so the map can't grow forever.
// Caller must hold l.mu.
func (l *loginLimiter) evict(now time.Time) {
	for ip, e := range l.bins {
		if now.Sub(e.seen) > time.Hour {
			delete(l.bins, ip)
		}
	}
}

// strikeLimiter locks out a client IP after N failed attempts for a fixed
// window. Used by the viewer gate: 3 wrong passwords → 15 min lockout.
type strikeLimiter struct {
	mu         sync.Mutex
	maxStrikes int
	lockFor    time.Duration
	entries    map[string]*strikeEntry
}

type strikeEntry struct {
	fails       int
	lockedUntil time.Time
	seen        time.Time
}

func newStrikeLimiter(maxStrikes int, lockFor time.Duration) *strikeLimiter {
	return &strikeLimiter{maxStrikes: maxStrikes, lockFor: lockFor, entries: map[string]*strikeEntry{}}
}

// locked reports whether the IP is currently locked out, and for how long.
func (s *strikeLimiter) locked(ip string) (bool, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.evict(now)
	e, ok := s.entries[ip]
	if !ok {
		return false, 0
	}
	e.seen = now
	if now.Before(e.lockedUntil) {
		return true, time.Until(e.lockedUntil)
	}
	return false, 0
}

// fail records a failed attempt and returns true if the IP is now locked out.
func (s *strikeLimiter) fail(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	e, ok := s.entries[ip]
	if !ok {
		e = &strikeEntry{}
		s.entries[ip] = e
	}
	e.seen = now
	e.fails++
	if e.fails >= s.maxStrikes {
		e.lockedUntil = now.Add(s.lockFor)
		e.fails = 0
	}
	return now.Before(e.lockedUntil)
}

// success clears any recorded strikes for the IP.
func (s *strikeLimiter) success(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, ip)
}

// evict drops stale, unlocked entries. Caller must hold s.mu.
func (s *strikeLimiter) evict(now time.Time) {
	for ip, e := range s.entries {
		if now.After(e.lockedUntil) && now.Sub(e.seen) > time.Hour {
			delete(s.entries, ip)
		}
	}
}

// clientIP returns the caller's IP. X-Forwarded-For / X-Real-IP are only trusted
// when the direct peer (RemoteAddr) is inside one of the trusted proxy CIDRs;
// otherwise they are attacker-controlled and ignored. With no trusted proxies
// configured, the real socket address is always used (unspoofable).
func clientIP(r *http.Request, trusted []*net.IPNet) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if len(trusted) == 0 || !ipInAny(host, trusted) {
		return host
	}
	// Peer is a trusted proxy — walk X-Forwarded-For right-to-left and return
	// the first address that is not itself a trusted proxy (the real client).
	if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
		parts := strings.Split(xf, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			ip := strings.TrimSpace(parts[i])
			if ip == "" {
				continue
			}
			if !ipInAny(ip, trusted) {
				return ip
			}
		}
	}
	if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
		return xr
	}
	return host
}

func ipInAny(ip string, nets []*net.IPNet) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, n := range nets {
		if n.Contains(parsed) {
			return true
		}
	}
	return false
}

// ParseTrustedProxies parses a comma-separated list of CIDRs or bare IPs.
// Invalid entries are skipped. Exported so main can build it once at boot.
func ParseTrustedProxies(s string) []*net.IPNet {
	var out []*net.IPNet
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !strings.Contains(part, "/") {
			if strings.Contains(part, ":") {
				part += "/128"
			} else {
				part += "/32"
			}
		}
		if _, n, err := net.ParseCIDR(part); err == nil {
			out = append(out, n)
		}
	}
	return out
}
