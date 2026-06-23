// Package auth implements passkey (WebAuthn) authentication for shellraiser.
//
// Model: a single box owner with one or more passkeys. Because a WebAuthn
// credential is bound to one RP ID (registrable domain), and shellraiser may be
// reached via localhost AND one or more tunnel hostnames, credentials are
// stored *per RP ID*, discovered from the request Host at registration time.
// Registering a passkey on a host where you have none requires the bootstrap
// code printed in the logs; once you hold a session you can add more.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

const (
	sessionCookie  = "shellraiser_session"
	ceremonyCookie = "shellraiser_cer"
	sessionTTL     = 30 * 24 * time.Hour
	ceremonyTTL    = 5 * time.Minute
)

// storedCred is a credential plus the RP ID it was registered under.
type storedCred struct {
	RPID      string              `json:"rpId"`
	Label     string              `json:"label"`
	CreatedAt time.Time           `json:"createdAt"`
	Cred      webauthn.Credential `json:"cred"`
}

type store struct {
	UserID    []byte       `json:"userId"`
	Bootstrap string       `json:"bootstrap"`
	Creds     []storedCred `json:"creds"`
}

type ceremony struct {
	session   webauthn.SessionData
	rpID      string
	label     string
	bootstrap bool // gated by the bootstrap code (vs. an existing session)
	expires   time.Time
}

// Manager holds auth state and serves the /api/auth/* endpoints.
type Manager struct {
	path       string
	noAuth     bool
	token      string                        // optional SHELLRAISER_TOKEN fallback for automation
	rpOverride string                        // pinned RP ID (else discovered from Host)
	Logf       func(format string, a ...any) // optional; for logging bootstrap rotation

	mu           sync.Mutex
	data         store
	sessions     map[string]time.Time
	ceremonies   map[string]*ceremony
	regFails     int       // consecutive bad bootstrap-code attempts
	regLockUntil time.Time // registration locked until this time
}

const (
	maxBootstrapFails = 5
	bootstrapLockout  = 5 * time.Minute
)

// bootstrapLocked reports whether registration is temporarily rate-limited.
func (m *Manager) bootstrapLocked() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return time.Now().Before(m.regLockUntil)
}

// bootstrapResult records a bootstrap-code attempt, locking out after too many
// consecutive failures.
func (m *Manager) bootstrapResult(ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ok {
		m.regFails = 0
		return
	}
	m.regFails++
	if m.regFails >= maxBootstrapFails {
		m.regLockUntil = time.Now().Add(bootstrapLockout)
		m.regFails = 0
	}
}

// New loads (or initializes) the auth store at path. rpOverride pins the
// WebAuthn RP ID; when empty it is discovered from each request's Host.
func New(path, token, rpOverride string, noAuth bool) (*Manager, error) {
	m := &Manager{
		path: path, token: token, rpOverride: rpOverride, noAuth: noAuth,
		sessions: map[string]time.Time{}, ceremonies: map[string]*ceremony{},
	}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &m.data)
	}
	if len(m.data.UserID) == 0 {
		m.data.UserID = randomBytes(16)
	}
	if m.data.Bootstrap == "" {
		m.data.Bootstrap = randomCode()
	}
	return m, m.save()
}

// BootstrapCode returns the code that authorizes registering a new passkey.
func (m *Manager) BootstrapCode() string { return m.data.Bootstrap }

// HasCredentials reports whether any passkey is registered (on any host).
func (m *Manager) HasCredentials() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.data.Creds) > 0
}

// rotateBootstrap issues a fresh bootstrap code after one is used, so a code
// leaked via the logs can't be reused to enroll more passkeys.
func (m *Manager) rotateBootstrap() {
	m.mu.Lock()
	m.data.Bootstrap = randomCode()
	code := m.data.Bootstrap
	_ = m.save()
	m.mu.Unlock()
	if m.Logf != nil {
		m.Logf("bootstrap code used — new code: %s", code)
	}
}

// Enabled reports whether auth is enforced.
func (m *Manager) Enabled() bool { return !m.noAuth }

func (m *Manager) save() error {
	b, _ := json.MarshalIndent(m.data, "", "  ")
	return os.WriteFile(m.path, b, 0o600)
}

// Authenticated reports whether the request carries a valid session or token.
func (m *Manager) Authenticated(r *http.Request) bool {
	if m.noAuth {
		return true
	}
	if c, err := r.Cookie(sessionCookie); err == nil && m.validSession(c.Value) {
		return true
	}
	// Token fallback for automation — header only. We intentionally do NOT accept
	// it as a ?t= query param: that leaks the credential into access logs,
	// Referer headers, and browser history.
	if m.token != "" {
		if t := r.Header.Get("X-Shellraiser-Token"); t != "" && ctEq(t, m.token) {
			return true
		}
	}
	return false
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

// --- WebAuthn helpers ------------------------------------------------------

// rp returns the effective RP ID: the pinned override, or the request Host.
func (m *Manager) rp(r *http.Request) string {
	if m.rpOverride != "" {
		return m.rpOverride
	}
	return rpID(r)
}

func (m *Manager) webauthnFor(r *http.Request) (*webauthn.WebAuthn, error) {
	return webauthn.New(&webauthn.Config{
		RPID:          m.rp(r),
		RPDisplayName: "shellraiser",
		RPOrigins:     []string{origin(r)},
	})
}

// user returns the box owner scoped to the RP ID's credentials.
func (m *Manager) user(rp string) *boxUser {
	m.mu.Lock()
	defer m.mu.Unlock()
	var creds []webauthn.Credential
	for _, c := range m.data.Creds {
		if c.RPID == rp {
			creds = append(creds, c.Cred)
		}
	}
	return &boxUser{id: m.data.UserID, creds: creds}
}

func (m *Manager) credCount(rp string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, c := range m.data.Creds {
		if c.RPID == rp {
			n++
		}
	}
	return n
}

// boxUser implements webauthn.User for one RP ID.
type boxUser struct {
	id    []byte
	creds []webauthn.Credential
}

func (u *boxUser) WebAuthnID() []byte                         { return u.id }
func (u *boxUser) WebAuthnName() string                       { return "shellraiser" }
func (u *boxUser) WebAuthnDisplayName() string                { return "shellraiser owner" }
func (u *boxUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

// --- ceremony storage ------------------------------------------------------

func (m *Manager) putCeremony(w http.ResponseWriter, r *http.Request, c *ceremony) {
	id := hex.EncodeToString(randomBytes(16))
	c.expires = time.Now().Add(ceremonyTTL)
	m.mu.Lock()
	m.ceremonies[id] = c
	m.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name: ceremonyCookie, Value: id, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: isHTTPS(r), MaxAge: int(ceremonyTTL.Seconds()),
	})
}

func (m *Manager) takeCeremony(r *http.Request) *ceremony {
	c, err := r.Cookie(ceremonyCookie)
	if err != nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cer, ok := m.ceremonies[c.Value]
	if !ok || time.Now().After(cer.expires) {
		delete(m.ceremonies, c.Value)
		return nil
	}
	delete(m.ceremonies, c.Value)
	return cer
}

// --- small helpers ---------------------------------------------------------

func ctEq(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

// randomCode returns a friendly bootstrap code like "K7Q2-9FXM-3PWA".
func randomCode() string {
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

func rpID(r *http.Request) string {
	h := r.Host
	if i := strings.LastIndex(h, ":"); i >= 0 {
		h = h[:i]
	}
	return h
}

func origin(r *http.Request) string {
	return scheme(r) + "://" + r.Host
}

func scheme(r *http.Request) string {
	if isHTTPS(r) {
		return "https"
	}
	return "http"
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// descriptors lists existing credentials so registration can exclude them.
func descriptors(creds []webauthn.Credential) []protocol.CredentialDescriptor {
	out := make([]protocol.CredentialDescriptor, 0, len(creds))
	for _, c := range creds {
		out = append(out, protocol.CredentialDescriptor{
			Type:         protocol.PublicKeyCredentialType,
			CredentialID: c.ID,
		})
	}
	return out
}
