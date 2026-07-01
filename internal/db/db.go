package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

func Open(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)", path)
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	d.SetMaxOpenConns(1)
	if err := d.Ping(); err != nil {
		return nil, err
	}
	if err := migrate(d); err != nil {
		return nil, err
	}
	return d, nil
}

func migrate(d *sql.DB) error {
	if _, err := d.Exec(`CREATE TABLE IF NOT EXISTS _migrations (name TEXT PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return err
	}
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, n := range names {
		var count int
		if err := d.QueryRow(`SELECT COUNT(1) FROM _migrations WHERE name = ?`, n).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			continue
		}
		b, err := fs.ReadFile(migrationsFS, "migrations/"+n)
		if err != nil {
			return err
		}
		if _, err := d.Exec(string(b)); err != nil {
			return fmt.Errorf("migrate %s: %w", n, err)
		}
		if _, err := d.Exec(`INSERT INTO _migrations (name, applied_at) VALUES (?, ?)`, n, time.Now().Unix()); err != nil {
			return err
		}
	}
	return nil
}

type Column struct {
	ID, Name  string
	Sort      int
	DeletedAt int64
}

type Application struct {
	ID, Name, Icon, ColumnID string
	Sort                     int
	DeletedAt                int64
}

type Service struct {
	ID, AppID, Name string
	Sort            int
	DeletedAt       int64
}

type URL struct {
	ID, ServiceID, Name, URL, Alt string
	CheckEnabled                  bool
	IntervalSeconds               int
	Sort                          int
	DeletedAt                     int64
}

type Check struct {
	ID         int64
	URLID      string
	TS         int64
	OK         bool
	StatusCode int
	LatencyMS  int
	Error      string
}

func ListColumns(ctx context.Context, d *sql.DB) ([]Column, error) {
	rows, err := d.QueryContext(ctx, `SELECT id, name, sort FROM col WHERE deleted_at = 0 ORDER BY sort, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Column
	for rows.Next() {
		var c Column
		if err := rows.Scan(&c.ID, &c.Name, &c.Sort); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func ListApplications(ctx context.Context, d *sql.DB) ([]Application, error) {
	rows, err := d.QueryContext(ctx, `SELECT id, name, icon, sort, col_id FROM application WHERE deleted_at = 0 ORDER BY sort, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Application
	for rows.Next() {
		var a Application
		if err := rows.Scan(&a.ID, &a.Name, &a.Icon, &a.Sort, &a.ColumnID); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func ListServices(ctx context.Context, d *sql.DB) ([]Service, error) {
	rows, err := d.QueryContext(ctx, `SELECT id, app_id, name, sort FROM service WHERE deleted_at = 0 ORDER BY sort, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Service
	for rows.Next() {
		var s Service
		if err := rows.Scan(&s.ID, &s.AppID, &s.Name, &s.Sort); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func ListURLs(ctx context.Context, d *sql.DB) ([]URL, error) {
	rows, err := d.QueryContext(ctx, `SELECT id, service_id, name, url, alt_url, check_enabled, interval_seconds, sort FROM url WHERE deleted_at = 0 ORDER BY sort, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []URL
	for rows.Next() {
		var u URL
		var ce int
		if err := rows.Scan(&u.ID, &u.ServiceID, &u.Name, &u.URL, &u.Alt, &ce, &u.IntervalSeconds, &u.Sort); err != nil {
			return nil, err
		}
		u.CheckEnabled = ce == 1
		out = append(out, u)
	}
	return out, rows.Err()
}

func GetURL(ctx context.Context, d *sql.DB, id string) (*URL, error) {
	row := d.QueryRowContext(ctx, `SELECT id, service_id, name, url, alt_url, check_enabled, interval_seconds, sort FROM url WHERE id = ? AND deleted_at = 0`, id)
	var u URL
	var ce int
	if err := row.Scan(&u.ID, &u.ServiceID, &u.Name, &u.URL, &u.Alt, &ce, &u.IntervalSeconds, &u.Sort); err != nil {
		return nil, err
	}
	u.CheckEnabled = ce == 1
	return &u, nil
}

func LatestCheck(ctx context.Context, d *sql.DB, urlID string) (*Check, error) {
	row := d.QueryRowContext(ctx, `SELECT id, url_id, ts, ok, status_code, latency_ms, error FROM check_result WHERE url_id = ? ORDER BY ts DESC LIMIT 1`, urlID)
	var c Check
	var ok int
	if err := row.Scan(&c.ID, &c.URLID, &c.TS, &ok, &c.StatusCode, &c.LatencyMS, &c.Error); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	c.OK = ok == 1
	return &c, nil
}

func LatestChecksAll(ctx context.Context, d *sql.DB) (map[string]Check, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT cr.url_id, cr.ts, cr.ok, cr.status_code, cr.latency_ms, cr.error
		FROM check_result cr
		INNER JOIN (
			SELECT url_id, MAX(ts) AS mts FROM check_result GROUP BY url_id
		) m ON m.url_id = cr.url_id AND m.mts = cr.ts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]Check)
	for rows.Next() {
		var c Check
		var ok int
		if err := rows.Scan(&c.URLID, &c.TS, &ok, &c.StatusCode, &c.LatencyMS, &c.Error); err != nil {
			return nil, err
		}
		c.OK = ok == 1
		out[c.URLID] = c
	}
	return out, rows.Err()
}

func HistoryFor(ctx context.Context, d *sql.DB, urlID string, limit int) ([]Check, error) {
	rows, err := d.QueryContext(ctx, `SELECT id, url_id, ts, ok, status_code, latency_ms, error FROM check_result WHERE url_id = ? ORDER BY ts DESC LIMIT ?`, urlID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Check
	for rows.Next() {
		var c Check
		var ok int
		if err := rows.Scan(&c.ID, &c.URLID, &c.TS, &ok, &c.StatusCode, &c.LatencyMS, &c.Error); err != nil {
			return nil, err
		}
		c.OK = ok == 1
		out = append(out, c)
	}
	return out, rows.Err()
}

func InsertCheck(ctx context.Context, d *sql.DB, c Check) error {
	ok := 0
	if c.OK {
		ok = 1
	}
	_, err := d.ExecContext(ctx, `INSERT INTO check_result (url_id, ts, ok, status_code, latency_ms, error) VALUES (?, ?, ?, ?, ?, ?)`,
		c.URLID, c.TS, ok, c.StatusCode, c.LatencyMS, c.Error)
	return err
}

func PruneHistory(ctx context.Context, d *sql.DB, keepPerURL int) error {
	_, err := d.ExecContext(ctx, `
		DELETE FROM check_result
		WHERE id IN (
			SELECT id FROM (
				SELECT id, ROW_NUMBER() OVER (PARTITION BY url_id ORDER BY ts DESC) AS rn
				FROM check_result
			) WHERE rn > ?
		)`, keepPerURL)
	return err
}

func PruneSessions(ctx context.Context, d *sql.DB) error {
	now := time.Now().Unix()
	if _, err := d.ExecContext(ctx, `DELETE FROM session WHERE expires_at < ?`, now); err != nil {
		return err
	}
	_, err := d.ExecContext(ctx, `DELETE FROM gate_session WHERE expires_at < ?`, now)
	return err
}
