package http

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/krisk248/grail/internal/checker"
	"github.com/krisk248/grail/internal/config"
	"github.com/krisk248/grail/internal/db"
	"github.com/krisk248/grail/internal/session"
)

type Server struct {
	DB             *sql.DB
	Log            *slog.Logger
	Checker        *checker.Supervisor
	Sessions       *session.Manager
	ConfigPath     string
	AdminPassHash  []byte
	ViewerPassHash []byte
	StaticFS       fs.FS
	Current        *atomic.Pointer[config.Config]
	TrustedProxies []*net.IPNet
	UmamiScriptURL string
	SecureCookies  bool
	csp            string
	loginLimiter   *loginLimiter
	gateLimiter    *strikeLimiter
	OnConfigSaved  func()
}

// Config carries the tunables NewServer needs beyond its core dependencies.
type Options struct {
	AdminHash      []byte
	ViewerHash     []byte
	TrustedProxies []*net.IPNet
	UmamiScriptURL string
	SecureCookies  bool
}

func NewServer(d *sql.DB, log *slog.Logger, c *checker.Supervisor, sm *session.Manager, cfgPath string, staticFS fs.FS, current *atomic.Pointer[config.Config], opts Options, onSaved func()) *Server {
	// Hash-allowlist the inline scripts we actually ship (SvelteKit bootstrap in
	// index.html + the gate page) so the CSP can stay strict without
	// 'unsafe-inline'. Computed from the exact bytes served.
	hashes := inlineScriptHashes(gatePageHTML)
	if b, err := fs.ReadFile(staticFS, "index.html"); err == nil {
		hashes = append(hashes, inlineScriptHashes(string(b))...)
	}
	csp := buildCSP(cspSourceFromURL(opts.UmamiScriptURL), hashes)

	return &Server{
		DB: d, Log: log, Checker: c, Sessions: sm,
		ConfigPath: cfgPath, AdminPassHash: opts.AdminHash, ViewerPassHash: opts.ViewerHash,
		StaticFS:       staticFS,
		Current:        current,
		TrustedProxies: opts.TrustedProxies,
		UmamiScriptURL: opts.UmamiScriptURL,
		SecureCookies:  opts.SecureCookies,
		csp:            csp,
		loginLimiter:   newLoginLimiter(),
		gateLimiter:    newStrikeLimiter(3, 15*time.Minute),
		OnConfigSaved:  onSaved,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Public API
	mux.HandleFunc("GET /api/site", s.getSite)
	mux.HandleFunc("GET /api/state", s.getState)
	mux.HandleFunc("GET /api/url/{id}/history", s.getURLHistory)

	// Viewer gate (site-wide password wall)
	mux.HandleFunc("POST /gate/login", s.postGateLogin)

	// Admin API
	mux.HandleFunc("POST /api/url/{id}/check-now", s.requireAuth(s.postCheckNow))
	mux.HandleFunc("POST /admin/login", s.postLogin)
	mux.HandleFunc("POST /admin/logout", s.postLogout)
	mux.HandleFunc("POST /admin/logout-all", s.requireAuth(s.postLogoutAll))
	mux.HandleFunc("GET /admin/api/me", s.getMe)
	mux.HandleFunc("GET /admin/api/config", s.requireAuth(s.getConfig))
	mux.HandleFunc("POST /admin/api/config", s.requireAuth(s.postConfig))

	// SPA fallback: everything else serves the embedded SvelteKit static build.
	mux.HandleFunc("/", s.serveSPA)

	// gateMW is innermost so its 401 page still gets security headers + logging.
	return chain(mux,
		recoverMW(s.Log),
		loggerMW(s.Log),
		securityHeadersMW(s.csp, s.SecureCookies),
		s.gateMW,
	)
}

// ---------------- helpers ----------------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (s *Server) requireAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.Sessions.Validate(r.Context(), r) {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		// CSRF for state-changing requests
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if err := s.Sessions.CheckCSRF(r); err != nil {
				writeErr(w, http.StatusForbidden, "csrf: "+err.Error())
				return
			}
		}
		h(w, r)
	}
}

// ---------------- handlers ----------------

type urlOut struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	URL        string `json:"url"`
	Alt        string `json:"alt,omitempty"`
	Check      bool   `json:"check"`
	Interval   int    `json:"interval_seconds"`
	OK         *bool  `json:"ok,omitempty"`
	StatusCode int    `json:"status_code,omitempty"`
	LatencyMS  int    `json:"latency_ms,omitempty"`
	LastTS     int64  `json:"last_ts,omitempty"`
	Error      string `json:"error,omitempty"`
}

type svcOut struct {
	ID   string   `json:"id"`
	Name string   `json:"name"`
	URLs []urlOut `json:"urls"`
}

type appOut struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Icon     string   `json:"icon"`
	Services []svcOut `json:"services"`
}

type colOut struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Applications []appOut `json:"applications"`
}

type stateOut struct {
	Title       string   `json:"title"`
	Footer      string   `json:"footer"`
	Columns     []colOut `json:"columns"`
	GeneratedAt int64    `json:"generated_at"`
}

func (s *Server) siteTitle() string {
	if c := s.Current.Load(); c != nil && strings.TrimSpace(c.Site.Title) != "" {
		return c.Site.Title
	}
	return "grail"
}

func (s *Server) siteFooter() string {
	if c := s.Current.Load(); c != nil {
		return c.Site.Footer
	}
	return ""
}

func (s *Server) getSite(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]string{
		"title":            s.siteTitle(),
		"footer":           s.siteFooter(),
		"umami_script_url": os.Getenv("UMAMI_SCRIPT_URL"),
		"umami_website_id": os.Getenv("UMAMI_WEBSITE_ID"),
	})
}

func (s *Server) getState(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cols, err := db.ListColumns(ctx, s.DB)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	apps, err := db.ListApplications(ctx, s.DB)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	svcs, err := db.ListServices(ctx, s.DB)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	urls, err := db.ListURLs(ctx, s.DB)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	latest, err := db.LatestChecksAll(ctx, s.DB)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}

	urlsBySvc := map[string][]urlOut{}
	for _, u := range urls {
		out := urlOut{
			ID: u.ID, Name: u.Name, URL: u.URL, Alt: u.Alt,
			Check: u.CheckEnabled, Interval: u.IntervalSeconds,
		}
		if c, ok := latest[u.ID]; ok {
			b := c.OK
			out.OK = &b
			out.StatusCode = c.StatusCode
			out.LatencyMS = c.LatencyMS
			out.LastTS = c.TS
			out.Error = c.Error
		}
		urlsBySvc[u.ServiceID] = append(urlsBySvc[u.ServiceID], out)
	}
	svcsByApp := map[string][]svcOut{}
	for _, sv := range svcs {
		svcsByApp[sv.AppID] = append(svcsByApp[sv.AppID], svcOut{
			ID: sv.ID, Name: sv.Name, URLs: urlsBySvc[sv.ID],
		})
	}
	appsByCol := map[string][]appOut{}
	for _, a := range apps {
		appsByCol[a.ColumnID] = append(appsByCol[a.ColumnID], appOut{
			ID: a.ID, Name: a.Name, Icon: a.Icon, Services: svcsByApp[a.ID],
		})
	}

	out := stateOut{Title: s.siteTitle(), Footer: s.siteFooter(), GeneratedAt: time.Now().Unix()}
	if len(cols) == 0 {
		// No user-defined columns — render everything in one implicit column.
		var allApps []appOut
		for _, a := range apps {
			allApps = append(allApps, appOut{
				ID: a.ID, Name: a.Name, Icon: a.Icon, Services: svcsByApp[a.ID],
			})
		}
		out.Columns = []colOut{{ID: "", Name: "", Applications: allApps}}
	} else {
		for _, c := range cols {
			out.Columns = append(out.Columns, colOut{
				ID: c.ID, Name: c.Name, Applications: appsByCol[c.ID],
			})
		}
		// Catch any apps whose col_id no longer matches a column (shouldn't happen
		// after a successful reconcile, but defensive).
		var orphans []appOut
		known := map[string]bool{}
		for _, c := range cols {
			known[c.ID] = true
		}
		for cid, list := range appsByCol {
			if !known[cid] {
				orphans = append(orphans, list...)
			}
		}
		if len(orphans) > 0 {
			out.Columns = append(out.Columns, colOut{ID: "__orphans", Name: "Unassigned", Applications: orphans})
		}
	}
	writeJSON(w, 200, out)
}

func (s *Server) getURLHistory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	hist, err := db.HistoryFor(r.Context(), s.DB, id, 100)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	type histRow struct {
		TS         int64  `json:"ts"`
		OK         bool   `json:"ok"`
		StatusCode int    `json:"status_code"`
		LatencyMS  int    `json:"latency_ms"`
		Error      string `json:"error"`
	}
	out := make([]histRow, 0, len(hist))
	for _, c := range hist {
		out = append(out, histRow{TS: c.TS, OK: c.OK, StatusCode: c.StatusCode, LatencyMS: c.LatencyMS, Error: c.Error})
	}
	writeJSON(w, 200, out)
}

func (s *Server) postCheckNow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := s.Checker.CheckNow(ctx, id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

type loginReq struct {
	Password string `json:"password"`
}

func (s *Server) postLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r, s.TrustedProxies)
	if !s.loginLimiter.allow(ip) {
		writeErr(w, http.StatusTooManyRequests, "too many login attempts")
		return
	}
	var req loginReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		writeErr(w, 400, "bad body")
		return
	}
	if err := bcrypt.CompareHashAndPassword(s.AdminPassHash, []byte(req.Password)); err != nil {
		// constant-ish failure delay to make spraying obvious
		time.Sleep(250 * time.Millisecond)
		s.Log.Warn("admin login failed", "ip", ip)
		writeErr(w, http.StatusUnauthorized, "invalid password")
		return
	}
	// Rotate: drop any prior session for this browser before minting a new one.
	s.Sessions.Destroy(r.Context(), w, r)
	if _, err := s.Sessions.Create(r.Context(), w); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.Log.Info("admin login ok", "ip", ip)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) postLogout(w http.ResponseWriter, r *http.Request) {
	s.Sessions.Destroy(r.Context(), w, r)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// postLogoutAll invalidates every admin session (log out everywhere).
func (s *Server) postLogoutAll(w http.ResponseWriter, r *http.Request) {
	if err := s.Sessions.DestroyAll(r.Context()); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.Sessions.Destroy(r.Context(), w, r)
	s.Log.Info("admin logout-all", "ip", clientIP(r, s.TrustedProxies))
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) getMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]bool{"authenticated": s.Sessions.Validate(r.Context(), r)})
}

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	b, err := os.ReadFile(s.ConfigPath)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"toml": string(b)})
}

type postConfigReq struct {
	TOML string `json:"toml"`
}

func (s *Server) postConfig(w http.ResponseWriter, r *http.Request) {
	var req postConfigReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, 400, "bad body")
		return
	}
	// Validate first
	if _, err := config.Parse([]byte(req.TOML)); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := config.AtomicWrite(s.ConfigPath, []byte(req.TOML)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if s.OnConfigSaved != nil {
		s.OnConfigSaved()
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// serveSPA serves the embedded SvelteKit static build with SPA fallback.
func (s *Server) serveSPA(w http.ResponseWriter, r *http.Request) {
	// Block obvious API misses from falling back to HTML.
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/admin/api/") {
		http.NotFound(w, r)
		return
	}
	clean := strings.TrimPrefix(r.URL.Path, "/")
	if clean == "" {
		clean = "index.html"
	}
	if f, err := s.StaticFS.Open(clean); err == nil {
		if st, _ := f.Stat(); st != nil && !st.IsDir() {
			http.ServeFileFS(w, r, s.StaticFS, clean)
			f.Close()
			return
		}
		f.Close()
	}
	// Fallback to index.html
	http.ServeFileFS(w, r, s.StaticFS, "index.html")
}
