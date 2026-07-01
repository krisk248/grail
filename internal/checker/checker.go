package checker

import (
	"context"
	"crypto/tls"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"runtime/debug"
	"sync"
	"time"

	"github.com/krisk248/grail/internal/db"
)

type Supervisor struct {
	db     *sql.DB
	log    *slog.Logger
	client *http.Client

	mu      sync.Mutex
	workers map[string]*worker

	lastMu sync.Mutex
	lastOK map[string]bool // url_id -> last observed up/down, for transition logging
}

type worker struct {
	id       string
	url      string
	interval time.Duration
	cancel   context.CancelFunc
}

func New(d *sql.DB, log *slog.Logger, insecureSkipVerify bool) *Supervisor {
	tr := &http.Transport{
		MaxIdleConns:        20,
		MaxIdleConnsPerHost: 2,
		IdleConnTimeout:     30 * time.Second,
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: insecureSkipVerify},
	}
	c := &http.Client{
		Timeout:   10 * time.Second,
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	return &Supervisor{
		db:      d,
		log:     log,
		client:  c,
		workers: make(map[string]*worker),
		lastOK:  make(map[string]bool),
	}
}

// Apply replaces the worker set with the given URLs (start new, update changed, stop missing).
func (s *Supervisor) Apply(ctx context.Context, urls []db.URL) {
	s.mu.Lock()
	defer s.mu.Unlock()
	desired := map[string]db.URL{}
	for _, u := range urls {
		if u.CheckEnabled {
			desired[u.ID] = u
		}
	}
	// Stop workers that should no longer run, or whose config changed.
	for id, w := range s.workers {
		du, ok := desired[id]
		if !ok || du.URL != w.url || time.Duration(du.IntervalSeconds)*time.Second != w.interval {
			w.cancel()
			delete(s.workers, id)
		}
	}
	// Start new or updated workers.
	for id, u := range desired {
		if _, ok := s.workers[id]; ok {
			continue
		}
		wctx, cancel := context.WithCancel(ctx)
		w := &worker{
			id:       id,
			url:      u.URL,
			interval: time.Duration(u.IntervalSeconds) * time.Second,
			cancel:   cancel,
		}
		s.workers[id] = w
		go s.run(wctx, w)
	}
}

// CheckNow performs an out-of-band check for a single URL.
func (s *Supervisor) CheckNow(ctx context.Context, urlID string) error {
	u, err := db.GetURL(ctx, s.db, urlID)
	if err != nil {
		return err
	}
	if u == nil {
		return fmt.Errorf("url not found")
	}
	s.do(ctx, u.ID, u.URL)
	return nil
}

func (s *Supervisor) StopAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, w := range s.workers {
		w.cancel()
	}
	s.workers = map[string]*worker{}
}

func (s *Supervisor) run(ctx context.Context, w *worker) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("checker panic", "url_id", w.id, "panic", r, "stack", string(debug.Stack()))
		}
	}()
	// Jitter first tick to avoid thundering herd.
	jitter := time.Duration(rand.Int63n(int64(w.interval)))
	timer := time.NewTimer(jitter)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			s.do(ctx, w.id, w.url)
			timer.Reset(w.interval)
		}
	}
}

func (s *Supervisor) do(ctx context.Context, urlID, target string) {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
	if err != nil {
		s.record(urlID, target, false, 0, time.Since(start), err.Error())
		return
	}
	req.Header.Set("User-Agent", "grail/1.0 (uptime-check)")
	resp, err := s.client.Do(req)
	latency := time.Since(start)
	if err != nil {
		s.record(urlID, target, false, 0, latency, err.Error())
		return
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	ok := resp.StatusCode >= 200 && resp.StatusCode < 400
	s.record(urlID, target, ok, resp.StatusCode, latency, "")
}

func (s *Supervisor) record(urlID, target string, ok bool, code int, latency time.Duration, errStr string) {
	// Log only state transitions (up<->down) so the reason a URL is down is
	// visible in the logs without spamming a line every interval.
	s.lastMu.Lock()
	prev, had := s.lastOK[urlID]
	s.lastOK[urlID] = ok
	s.lastMu.Unlock()
	switch {
	case !ok && (!had || prev):
		s.log.Warn("check down", "url_id", urlID, "url", target, "status", code, "err", errStr)
	case ok && had && !prev:
		s.log.Info("check recovered", "url_id", urlID, "url", target, "status", code, "latency_ms", latency.Milliseconds())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.InsertCheck(ctx, s.db, db.Check{
		URLID:      urlID,
		TS:         time.Now().Unix(),
		OK:         ok,
		StatusCode: code,
		LatencyMS:  int(latency.Milliseconds()),
		Error:      errStr,
	}); err != nil {
		s.log.Warn("insert check failed", "url_id", urlID, "err", err)
	}
}

// PruneLoop periodically prunes old check_result rows.
func (s *Supervisor) PruneLoop(ctx context.Context, keepPerURL int, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if err := db.PruneHistory(pctx, s.db, keepPerURL); err != nil {
				s.log.Warn("prune history failed", "err", err)
			}
			if err := db.PruneSessions(pctx, s.db); err != nil {
				s.log.Warn("prune sessions failed", "err", err)
			}
			cancel()
		}
	}
}
