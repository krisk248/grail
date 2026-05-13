package config

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/pelletier/go-toml/v2"

	"github.com/krisk248/grail/internal/db"
)

type Config struct {
	Site         Site          `toml:"site"`
	Columns      []Column      `toml:"column"`
	Applications []Application `toml:"application"`
}

type Site struct {
	Title  string `toml:"title"`
	Footer string `toml:"footer"`
}

type Column struct {
	ID   string `toml:"id"`
	Name string `toml:"name"`
	Sort int    `toml:"sort"`
}

type Application struct {
	ID       string    `toml:"id"`
	Name     string    `toml:"name"`
	Icon     string    `toml:"icon"`
	Sort     int       `toml:"sort"`
	Column   string    `toml:"column"`
	URLs     []URL     `toml:"url"`     // direct URLs (no service grouping)
	Services []Service `toml:"service"`
}

type Service struct {
	ID   string `toml:"id"`
	Name string `toml:"name"`
	Sort int    `toml:"sort"`
	URLs []URL  `toml:"url"`
}

type URL struct {
	ID       string `toml:"id"`
	Name     string `toml:"name"`
	URL      string `toml:"url"`
	Alt      string `toml:"alt"`
	Check    *bool  `toml:"check"`
	Interval string `toml:"interval"`
	Sort     int    `toml:"sort"`
}

var idRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-_]*$`)

func (c *Config) Validate() error {
	seenCol := map[string]bool{}
	for ci, col := range c.Columns {
		if !idRE.MatchString(col.ID) {
			return fmt.Errorf("column[%d]: id %q must be lowercase letters/digits/-/_ and start with alphanum", ci, col.ID)
		}
		if seenCol[col.ID] {
			return fmt.Errorf("duplicate column id %q", col.ID)
		}
		seenCol[col.ID] = true
		if strings.TrimSpace(col.Name) == "" {
			return fmt.Errorf("column[%s]: name is required", col.ID)
		}
	}
	requireColumn := len(c.Columns) > 0

	seenApp := map[string]bool{}
	seenSvc := map[string]bool{}
	seenURL := map[string]bool{}

	for ai, a := range c.Applications {
		if !idRE.MatchString(a.ID) {
			return fmt.Errorf("application[%d]: id %q must be lowercase letters/digits/-/_ and start with alphanum", ai, a.ID)
		}
		if seenApp[a.ID] {
			return fmt.Errorf("duplicate application id %q", a.ID)
		}
		seenApp[a.ID] = true
		if strings.TrimSpace(a.Name) == "" {
			return fmt.Errorf("application[%s]: name is required", a.ID)
		}
		if requireColumn {
			if a.Column == "" {
				return fmt.Errorf("application[%s]: column field is required (columns are defined)", a.ID)
			}
			if !seenCol[a.Column] {
				return fmt.Errorf("application[%s]: column %q does not exist", a.ID, a.Column)
			}
		}
		// Direct URLs on the application (no service grouping).
		for ui, u := range a.URLs {
			if err := validateURL(u, fmt.Sprintf("application[%s].url[%d]", a.ID, ui)); err != nil {
				return err
			}
			if seenURL[u.ID] {
				return fmt.Errorf("duplicate url id %q", u.ID)
			}
			seenURL[u.ID] = true
		}
		// Services with their own URLs.
		for si, s := range a.Services {
			if !idRE.MatchString(s.ID) {
				return fmt.Errorf("application[%s].service[%d]: id %q invalid", a.ID, si, s.ID)
			}
			if seenSvc[s.ID] {
				return fmt.Errorf("duplicate service id %q", s.ID)
			}
			seenSvc[s.ID] = true
			// Service name can be empty — the UI hides the header in that case.
			for ui, u := range s.URLs {
				if err := validateURL(u, fmt.Sprintf("application[%s].service[%s].url[%d]", a.ID, s.ID, ui)); err != nil {
					return err
				}
				if seenURL[u.ID] {
					return fmt.Errorf("duplicate url id %q", u.ID)
				}
				seenURL[u.ID] = true
			}
		}
	}
	return nil
}

func validateURL(u URL, ctx string) error {
	if !idRE.MatchString(u.ID) {
		return fmt.Errorf("%s: id %q must be lowercase letters/digits/-/_", ctx, u.ID)
	}
	if strings.TrimSpace(u.Name) == "" {
		return fmt.Errorf("%s: name is required", ctx)
	}
	if strings.TrimSpace(u.URL) == "" {
		return fmt.Errorf("%s: url is required", ctx)
	}
	if u.Interval != "" {
		if _, err := time.ParseDuration(u.Interval); err != nil {
			return fmt.Errorf("%s: interval %q: %v", ctx, u.Interval, err)
		}
	}
	return nil
}

func Parse(data []byte) (*Config, error) {
	var c Config
	if err := toml.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

func AtomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".config-*.toml")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, path)
}

// Diff represents what changed between current DB state and a new Config.
type Diff struct {
	UpsertedURLs []db.URL
	RemovedURLs  []string
}

func intervalSeconds(s string) int {
	if s == "" {
		return 60
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 60
	}
	return int(d.Seconds())
}

func checkEnabled(u URL) bool {
	if u.Check == nil {
		return true
	}
	return *u.Check
}

// Reconcile syncs the DB to match cfg. Returns a Diff describing what changed for the checker.
func Reconcile(ctx context.Context, d *sql.DB, cfg *Config) (*Diff, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	now := time.Now().Unix()
	diff := &Diff{}

	desiredColIDs := map[string]bool{}
	desiredAppIDs := map[string]bool{}
	desiredSvcIDs := map[string]bool{}
	desiredURLIDs := map[string]bool{}

	// Columns
	for ci, col := range cfg.Columns {
		desiredColIDs[col.ID] = true
		sortVal := col.Sort
		if sortVal == 0 {
			sortVal = ci + 1
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO col (id, name, sort, deleted_at) VALUES (?, ?, ?, 0)
			ON CONFLICT(id) DO UPDATE SET name=excluded.name, sort=excluded.sort, deleted_at=0`,
			col.ID, col.Name, sortVal); err != nil {
			return nil, err
		}
	}

	for ai, a := range cfg.Applications {
		desiredAppIDs[a.ID] = true
		appSort := a.Sort
		if appSort == 0 {
			appSort = ai + 1
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO application (id, name, icon, sort, col_id, deleted_at) VALUES (?, ?, ?, ?, ?, 0)
			ON CONFLICT(id) DO UPDATE SET name=excluded.name, icon=excluded.icon, sort=excluded.sort, col_id=excluded.col_id, deleted_at=0`,
			a.ID, a.Name, a.Icon, appSort, a.Column); err != nil {
			return nil, err
		}

		// Direct URLs on an application get a synthetic service (empty name → hidden in UI).
		if len(a.URLs) > 0 {
			synthID := "__d_" + a.ID
			desiredSvcIDs[synthID] = true
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO service (id, app_id, name, sort, deleted_at) VALUES (?, ?, '', 0, 0)
				ON CONFLICT(id) DO UPDATE SET app_id=excluded.app_id, name='', sort=0, deleted_at=0`,
				synthID, a.ID); err != nil {
				return nil, err
			}
			for ui, u := range a.URLs {
				desiredURLIDs[u.ID] = true
				ce := 0
				if checkEnabled(u) {
					ce = 1
				}
				iv := intervalSeconds(u.Interval)
				uSort := u.Sort
				if uSort == 0 {
					uSort = ui + 1
				}
				if _, err := tx.ExecContext(ctx, `
					INSERT INTO url (id, service_id, name, url, alt_url, check_enabled, interval_seconds, sort, deleted_at)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0)
					ON CONFLICT(id) DO UPDATE SET
						service_id=excluded.service_id, name=excluded.name, url=excluded.url, alt_url=excluded.alt_url,
						check_enabled=excluded.check_enabled, interval_seconds=excluded.interval_seconds,
						sort=excluded.sort, deleted_at=0`,
					u.ID, synthID, u.Name, u.URL, u.Alt, ce, iv, uSort); err != nil {
					return nil, err
				}
				diff.UpsertedURLs = append(diff.UpsertedURLs, db.URL{
					ID: u.ID, ServiceID: synthID, Name: u.Name, URL: u.URL, Alt: u.Alt,
					CheckEnabled: checkEnabled(u), IntervalSeconds: iv, Sort: uSort,
				})
			}
		}

		// Regular service blocks.
		for si, s := range a.Services {
			desiredSvcIDs[s.ID] = true
			sSort := s.Sort
			if sSort == 0 {
				sSort = si + 1
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO service (id, app_id, name, sort, deleted_at) VALUES (?, ?, ?, ?, 0)
				ON CONFLICT(id) DO UPDATE SET app_id=excluded.app_id, name=excluded.name, sort=excluded.sort, deleted_at=0`,
				s.ID, a.ID, s.Name, sSort); err != nil {
				return nil, err
			}
			for ui, u := range s.URLs {
				desiredURLIDs[u.ID] = true
				ce := 0
				if checkEnabled(u) {
					ce = 1
				}
				iv := intervalSeconds(u.Interval)
				uSort := u.Sort
				if uSort == 0 {
					uSort = ui + 1
				}
				if _, err := tx.ExecContext(ctx, `
					INSERT INTO url (id, service_id, name, url, alt_url, check_enabled, interval_seconds, sort, deleted_at)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0)
					ON CONFLICT(id) DO UPDATE SET
						service_id=excluded.service_id, name=excluded.name, url=excluded.url, alt_url=excluded.alt_url,
						check_enabled=excluded.check_enabled, interval_seconds=excluded.interval_seconds,
						sort=excluded.sort, deleted_at=0`,
					u.ID, s.ID, u.Name, u.URL, u.Alt, ce, iv, uSort); err != nil {
					return nil, err
				}
				diff.UpsertedURLs = append(diff.UpsertedURLs, db.URL{
					ID: u.ID, ServiceID: s.ID, Name: u.Name, URL: u.URL, Alt: u.Alt,
					CheckEnabled: checkEnabled(u), IntervalSeconds: iv, Sort: uSort,
				})
			}
		}
	}

	// Soft-delete URLs missing from desired set.
	rows, err := tx.QueryContext(ctx, `SELECT id FROM url WHERE deleted_at = 0`)
	if err != nil {
		return nil, err
	}
	var existing []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		existing = append(existing, id)
	}
	rows.Close()
	for _, id := range existing {
		if !desiredURLIDs[id] {
			if _, err := tx.ExecContext(ctx, `UPDATE url SET deleted_at = ? WHERE id = ?`, now, id); err != nil {
				return nil, err
			}
			diff.RemovedURLs = append(diff.RemovedURLs, id)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE service SET deleted_at = ? WHERE deleted_at = 0 AND id NOT IN (SELECT value FROM json_each(?))`,
		now, jsonArray(keys(desiredSvcIDs))); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE application SET deleted_at = ? WHERE deleted_at = 0 AND id NOT IN (SELECT value FROM json_each(?))`,
		now, jsonArray(keys(desiredAppIDs))); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE col SET deleted_at = ? WHERE deleted_at = 0 AND id NOT IN (SELECT value FROM json_each(?))`,
		now, jsonArray(keys(desiredColIDs))); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return diff, nil
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func jsonArray(ss []string) string {
	if len(ss) == 0 {
		return "[]"
	}
	var b strings.Builder
	b.WriteByte('[')
	for i, s := range ss {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		b.WriteString(strings.ReplaceAll(s, `"`, `\"`))
		b.WriteByte('"')
	}
	b.WriteByte(']')
	return b.String()
}

// Watcher watches a TOML file and calls onChange after a successful parse+validate.
type Watcher struct {
	path     string
	onChange func(*Config)
	log      *slog.Logger
	mu       sync.Mutex
	lastHash string
}

func NewWatcher(path string, log *slog.Logger, onChange func(*Config)) *Watcher {
	return &Watcher{path: path, onChange: onChange, log: log}
}

func (w *Watcher) Run(ctx context.Context) error {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer fw.Close()
	dir := filepath.Dir(w.path)
	if err := fw.Add(dir); err != nil {
		return err
	}
	debounce := time.NewTimer(0)
	if !debounce.Stop() {
		<-debounce.C
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-fw.Events:
			if !ok {
				return nil
			}
			if filepath.Clean(ev.Name) != filepath.Clean(w.path) {
				continue
			}
			if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
				continue
			}
			debounce.Reset(300 * time.Millisecond)
		case err, ok := <-fw.Errors:
			if !ok {
				return nil
			}
			w.log.Warn("fsnotify error", "err", err)
		case <-debounce.C:
			cfg, err := Load(w.path)
			if err != nil {
				w.log.Warn("config reload failed; keeping previous", "err", err)
				continue
			}
			w.log.Info("config reloaded", "applications", len(cfg.Applications))
			w.onChange(cfg)
		}
	}
}
