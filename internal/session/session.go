package session

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"time"
)

const (
	CookieName     = "grail_session"
	CSRFCookieName = "grail_csrf"
	CSRFHeader     = "X-CSRF-Token"
	ttl            = 7 * 24 * time.Hour
)

type Manager struct {
	db     *sql.DB
	secure bool
}

func New(d *sql.DB, secureCookies bool) *Manager {
	return &Manager{db: d, secure: secureCookies}
}

func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Create issues a new session, sets the cookie, and returns the token.
func (m *Manager) Create(ctx context.Context, w http.ResponseWriter) (string, error) {
	tok := randomToken()
	exp := time.Now().Add(ttl)
	if _, err := m.db.ExecContext(ctx, `INSERT INTO session (token, expires_at) VALUES (?, ?)`, tok, exp.Unix()); err != nil {
		return "", err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  exp,
	})
	csrf := randomToken()
	http.SetCookie(w, &http.Cookie{
		Name:     CSRFCookieName,
		Value:    csrf,
		Path:     "/",
		HttpOnly: false,
		Secure:   m.secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  exp,
	})
	return tok, nil
}

// Validate returns true if the request has a valid session cookie.
func (m *Manager) Validate(ctx context.Context, r *http.Request) bool {
	c, err := r.Cookie(CookieName)
	if err != nil || c.Value == "" {
		return false
	}
	var exp int64
	row := m.db.QueryRowContext(ctx, `SELECT expires_at FROM session WHERE token = ?`, c.Value)
	if err := row.Scan(&exp); err != nil {
		return false
	}
	return exp >= time.Now().Unix()
}

// Destroy deletes the session from DB and clears the cookies.
func (m *Manager) Destroy(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(CookieName); err == nil && c.Value != "" {
		_, _ = m.db.ExecContext(ctx, `DELETE FROM session WHERE token = ?`, c.Value)
	}
	expire := time.Unix(0, 0)
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", Expires: expire, MaxAge: -1, HttpOnly: true, Secure: m.secure, SameSite: http.SameSiteLaxMode})
	http.SetCookie(w, &http.Cookie{Name: CSRFCookieName, Value: "", Path: "/", Expires: expire, MaxAge: -1, Secure: m.secure, SameSite: http.SameSiteLaxMode})
}

// CheckCSRF validates that the X-CSRF-Token header matches the CSRF cookie.
func (m *Manager) CheckCSRF(r *http.Request) error {
	c, err := r.Cookie(CSRFCookieName)
	if err != nil || c.Value == "" {
		return errors.New("missing csrf cookie")
	}
	h := r.Header.Get(CSRFHeader)
	if h == "" {
		return errors.New("missing csrf header")
	}
	if subtle.ConstantTimeCompare([]byte(c.Value), []byte(h)) != 1 {
		return errors.New("csrf mismatch")
	}
	return nil
}
