package auth

import (
	"encoding/json"
	"errors"
	"net/http"
)

var errTooShort = errors.New("password must be at least 6 characters")

// Mount registers the auth endpoints on mux.
func (m *Manager) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/auth/status", m.handleStatus)
	mux.HandleFunc("POST /api/auth/login", m.handleLogin)
	mux.HandleFunc("POST /api/auth/password", m.handleSetPassword)
	mux.HandleFunc("POST /api/auth/logout", m.handleLogout)
}

func (m *Manager) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":         m.Enabled(),
		"authenticated":   m.Authenticated(r),
		"mustSetPassword": m.MustSetPassword(),
	})
}

func (m *Manager) handleLogin(w http.ResponseWriter, r *http.Request) {
	if m.locked() {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many attempts — wait a minute"})
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	ok := m.checkPassword(req.Password)
	m.recordAttempt(ok)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "wrong password"})
		return
	}
	m.mintSession(w, r)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mustSetPassword": m.MustSetPassword()})
}

// handleSetPassword sets (or changes) the password. Must be authenticated — the
// first-login flow logs in with the temp password, then immediately sets a real
// one; later you can change it from settings.
func (m *Manager) handleSetPassword(w http.ResponseWriter, r *http.Request) {
	if !m.Authenticated(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not signed in"})
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	if err := m.SetPassword(req.Password); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if m.Logf != nil {
		m.Logf("password updated")
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (m *Manager) handleLogout(w http.ResponseWriter, r *http.Request) {
	m.clearSession(w, r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
