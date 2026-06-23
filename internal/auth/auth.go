// Package auth implements simple password authentication for the shellraiser
// coordinator.
//
// Model: one password for the whole coordinator. Passkeys were dropped — they
// bind to an origin (localhost vs the tailnet MagicDNS name), which made the
// localhost↔tailnet story painful. A single password works identically on both.
//
// The password is a bcrypt hash in the global config. If none is set, a random
// one-time password is generated and printed to the log at startup; you log in
// with it and are prompted to set a real one (written back to the config).
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	sessionCookie = "shellraiser_session"
	sessionTTL    = 30 * 24 * time.Hour
)

// Manager holds auth state and serves the /api/auth/* endpoints.
type Manager struct {
	noAuth bool
	save   func(hash string) error       // persist a new bcrypt hash to global config
	Logf   func(format string, a ...any) // optional

	mu        sync.Mutex
	hash      string // bcrypt password hash ("" = no real password set yet)
	temp      string // random one-time password, used until a real one is set
	sessions  map[string]time.Time
	fails     int
	lockUntil time.Time
}

const (
	maxLoginFails = 8
	loginLockout  = 1 * time.Minute
)

// New builds a Manager. passwordHash is the stored bcrypt hash (may be empty).
// save persists a freshly-set hash to the global config. With no hash and auth
// enabled, a random one-time password is minted for first login.
func New(passwordHash string, save func(hash string) error, noAuth bool) *Manager {
	m := &Manager{
		noAuth: noAuth, hash: passwordHash, save: save,
		sessions: map[string]time.Time{},
	}
	if !noAuth && passwordHash == "" {
		m.temp = randomPassword()
	}
	return m
}

// Enabled reports whether auth is enforced.
func (m *Manager) Enabled() bool { return !m.noAuth }

// HasPassword reports whether a real (non-temporary) password is set.
func (m *Manager) HasPassword() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hash != ""
}

// MustSetPassword reports whether the user still needs to choose a password.
func (m *Manager) MustSetPassword() bool { return m.Enabled() && !m.HasPassword() }

// TempPassword returns the one-time startup password (empty once a real one set).
func (m *Manager) TempPassword() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.temp
}

// Authenticated reports whether the request carries a valid session.
func (m *Manager) Authenticated(r *http.Request) bool {
	if m.noAuth {
		return true
	}
	if c, err := r.Cookie(sessionCookie); err == nil && m.validSession(c.Value) {
		return true
	}
	return false
}

// checkPassword constant-time-compares against the bcrypt hash, or the temp
// password when no real one is set yet.
func (m *Manager) checkPassword(pw string) bool {
	m.mu.Lock()
	hash, temp := m.hash, m.temp
	m.mu.Unlock()
	if hash != "" {
		return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
	}
	if temp != "" {
		return subtle.ConstantTimeCompare([]byte(pw), []byte(temp)) == 1
	}
	return false
}

// SetPassword hashes and persists a new password, clearing the temp password.
func (m *Manager) SetPassword(pw string) error {
	if len(pw) < 6 {
		return errTooShort
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.hash = string(h)
	m.temp = ""
	m.mu.Unlock()
	if m.save != nil {
		return m.save(string(h))
	}
	return nil
}

func (m *Manager) locked() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return time.Now().Before(m.lockUntil)
}

func (m *Manager) recordAttempt(ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ok {
		m.fails = 0
		return
	}
	m.fails++
	if m.fails >= maxLoginFails {
		m.lockUntil = time.Now().Add(loginLockout)
		m.fails = 0
	}
}

func (m *Manager) validSession(tok string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	exp, ok := m.sessions[tok]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(m.sessions, tok)
		return false
	}
	return true
}

func (m *Manager) mintSession(w http.ResponseWriter, r *http.Request) {
	tok := hex.EncodeToString(randomBytes(24))
	m.mu.Lock()
	m.sessions[tok] = time.Now().Add(sessionTTL)
	m.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: tok, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: isHTTPS(r), MaxAge: int(sessionTTL.Seconds()),
	})
}

func (m *Manager) clearSession(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		m.mu.Lock()
		delete(m.sessions, c.Value)
		m.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, MaxAge: -1})
}

// --- helpers ---------------------------------------------------------------

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

// randomPassword returns a friendly one-time password like "K7Q2-9FXM-3PWA".
func randomPassword() string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := randomBytes(12)
	var sb strings.Builder
	for i, x := range b {
		if i > 0 && i%4 == 0 {
			sb.WriteByte('-')
		}
		sb.WriteByte(alphabet[int(x)%len(alphabet)])
	}
	return sb.String()
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
