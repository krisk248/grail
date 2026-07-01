package main

import (
	"context"
	"errors"
	"log/slog"
	stdhttp "net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/krisk248/grail/internal/checker"
	"github.com/krisk248/grail/internal/config"
	"github.com/krisk248/grail/internal/db"
	grailhttp "github.com/krisk248/grail/internal/http"
	"github.com/krisk248/grail/internal/session"
	"github.com/krisk248/grail/internal/web"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

const defaultConfigTOML = `# grail config — edit and save, the dashboard reloads automatically (~300 ms).
# Stable IDs make renames safe. Number of columns is implicit by [[column]] count.

[site]
title  = "Kannan Test TTS"
footer = "Kannan Test TTS · Internal Tools · contact@example.com"

# ----- Columns (top-level groupings, rendered side-by-side) -----
[[column]]
id   = "search"
name = "Search"

[[column]]
id   = "social"
name = "Social"

[[column]]
id   = "uae"
name = "UAE"

# ===================== SEARCH COLUMN =====================
[[application]]
column = "search"
id     = "google"
name   = "Google"
icon   = "search"

  # Apps can either group URLs under services...
  [[application.service]]
  id   = "google-workspace"
  name = "Workspace"

    [[application.service.url]]
    id   = "gmail"
    name = "Gmail"
    url  = "https://mail.google.com"

    [[application.service.url]]
    id   = "drive"
    name = "Drive"
    url  = "https://drive.google.com"

  [[application.service]]
  id   = "google-media"
  name = "Media"

    [[application.service.url]]
    id   = "youtube"
    name = "YouTube"
    url  = "https://www.youtube.com"

    [[application.service.url]]
    id   = "google-images"
    name = "Images"
    url  = "https://images.google.com"

[[application]]
column = "search"
id     = "yahoo"
name   = "Yahoo"

  # ...or skip services entirely and put URLs directly on the application.
  [[application.url]]
  id   = "yahoo-search"
  name = "Search"
  url  = "https://search.yahoo.com"

  [[application.url]]
  id   = "yahoo-news"
  name = "News"
  url  = "https://news.yahoo.com"

# ===================== SOCIAL COLUMN =====================
[[application]]
column = "social"
id     = "github"
name   = "GitHub"

  [[application.url]]
  id   = "github-web"
  name = "Web"
  url  = "https://github.com"

[[application]]
column = "social"
id     = "ai-tools"
name   = "AI"

  [[application.url]]
  id   = "gemini"
  name = "Gemini"
  url  = "https://gemini.google.com"

  [[application.url]]
  id   = "claude"
  name = "Claude"
  url  = "https://claude.ai"

# ===================== UAE COLUMN =====================
[[application]]
column = "uae"
id     = "adx"
name   = "ADX"

  [[application.url]]
  id   = "adx-main"
  name = "Main"
  url  = "https://www.adx.ae"

  [[application.url]]
  id   = "adx-trading"
  name = "Trading"
  url  = "https://trading.adx.ae"

[[application]]
column = "uae"
id     = "mock-ipo"
name   = "Mock IPO"

  [[application.url]]
  id   = "mock-ipo-test"
  name = "Test"
  url  = "https://example.com"

[[application]]
column = "uae"
id     = "grafana"
name   = "Grafana"

  # Each URL can have an optional alt (e.g. the http counterpart of an https URL).
  [[application.url]]
  id   = "grafana-local"
  name = "Local"
  url  = "https://grafana.local:3000"
  alt  = "http://grafana.local:3000"
`


func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	dataDir := env("DATA_DIR", "./data")
	port := env("PORT", "8080")
	pass := os.Getenv("ADMIN_PASSWORD")
	if pass == "" {
		log.Error("ADMIN_PASSWORD env var is required")
		os.Exit(1)
	}
	if strings.EqualFold(pass, "changeme") {
		log.Error("ADMIN_PASSWORD is still the placeholder 'changeme' — set a real password")
		os.Exit(1)
	}
	// Viewer gate password (site-wide password wall). Defaults so the box works
	// out of the box; override VIEWER_PASSWORD per deployment.
	viewerPass := env("VIEWER_PASSWORD", "T0talt3ch25#")
	secureCookies := strings.EqualFold(os.Getenv("SECURE_COOKIES"), "true")
	insecureSkipVerify := strings.EqualFold(os.Getenv("INSECURE_SKIP_VERIFY"), "true")
	trustedProxies := grailhttp.ParseTrustedProxies(os.Getenv("TRUSTED_PROXIES"))

	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Error("mkdir data dir", "err", err)
		os.Exit(1)
	}
	dbPath := filepath.Join(dataDir, "grail.db")
	cfgPath := filepath.Join(dataDir, "config.toml")

	// Seed an example config if missing.
	if _, err := os.Stat(cfgPath); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(cfgPath, []byte(defaultConfigTOML), 0o644); err != nil {
			log.Error("seed config", "err", err)
			os.Exit(1)
		}
		log.Info("seeded example config", "path", cfgPath)
	}

	d, err := db.Open(dbPath)
	if err != nil {
		log.Error("open db", "err", err)
		os.Exit(1)
	}
	defer d.Close()

	// Load + reconcile initial config (refuse to start on first-load failure).
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Error("load config", "err", err)
		os.Exit(1)
	}

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	chk := checker.New(d, log, insecureSkipVerify)
	defer chk.StopAll()

	// Hash passwords once at boot.
	hash, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
	if err != nil {
		log.Error("hash password", "err", err)
		os.Exit(1)
	}
	viewerHash, err := bcrypt.GenerateFromPassword([]byte(viewerPass), bcrypt.DefaultCost)
	if err != nil {
		log.Error("hash viewer password", "err", err)
		os.Exit(1)
	}
	sm := session.New(d, secureCookies)

	// Reconcile + apply checker. Holds the latest valid config so handlers can read it.
	var current atomic.Pointer[config.Config]
	var reconcileMu sync.Mutex
	apply := func(c *config.Config) {
		reconcileMu.Lock()
		defer reconcileMu.Unlock()
		if _, err := config.Reconcile(rootCtx, d, c); err != nil {
			log.Error("reconcile failed", "err", err)
			return
		}
		urls, err := db.ListURLs(rootCtx, d)
		if err != nil {
			log.Error("list urls", "err", err)
			return
		}
		chk.Apply(rootCtx, urls)
		current.Store(c)
	}
	apply(cfg)

	watcher := config.NewWatcher(cfgPath, log, apply)
	go func() {
		if err := watcher.Run(rootCtx); err != nil {
			log.Warn("watcher exited", "err", err)
		}
	}()

	go chk.PruneLoop(rootCtx, 100, 1*time.Hour)

	srv := grailhttp.NewServer(d, log, chk, sm, cfgPath, web.Dist(), &current, grailhttp.Options{
		AdminHash:      hash,
		ViewerHash:     viewerHash,
		TrustedProxies: trustedProxies,
		UmamiScriptURL: os.Getenv("UMAMI_SCRIPT_URL"),
		SecureCookies:  secureCookies,
	}, func() {
		// On admin save, the fsnotify watcher will fire too — this is a fast-path nudge.
		if c, err := config.Load(cfgPath); err == nil {
			apply(c)
		}
	})

	httpServer := &stdhttp.Server{
		Addr:              ":" + port,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		log.Info("listening", "addr", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, stdhttp.ErrServerClosed) {
			log.Error("http server", "err", err)
			stop()
		}
	}()

	<-rootCtx.Done()
	log.Info("shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutCtx)
}
