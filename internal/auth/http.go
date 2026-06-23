package auth

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

// Mount registers the /api/auth/* endpoints (all unauthenticated entry points).
func (m *Manager) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/auth/status", m.handleStatus)
	mux.HandleFunc("POST /api/auth/register/begin", m.handleRegisterBegin)
	mux.HandleFunc("POST /api/auth/register/finish", m.handleRegisterFinish)
	mux.HandleFunc("POST /api/auth/login/begin", m.handleLoginBegin)
	mux.HandleFunc("POST /api/auth/login/finish", m.handleLoginFinish)
	mux.HandleFunc("POST /api/auth/logout", m.handleLogout)
	mux.HandleFunc("GET /api/auth/credentials", m.handleListCreds)
	mux.HandleFunc("DELETE /api/auth/credentials/{id}", m.handleDeleteCred)
}

func (m *Manager) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":       m.Enabled(),
		"authenticated": m.Authenticated(r),
		"registered":    m.credCount(m.rp(r)) > 0,
		"rpId":          m.rp(r),
	})
}

func (m *Manager) handleRegisterBegin(w http.ResponseWriter, r *http.Request) {
	var req struct{ Code, Label string }
	_ = json.NewDecoder(r.Body).Decode(&req)
	// Registering a passkey on a host requires the bootstrap code, unless the
	// caller already holds a valid session (adding an additional passkey).
	// Bad-code attempts are rate-limited to blunt brute force.
	authed := m.Authenticated(r)
	if !authed {
		if m.bootstrapLocked() {
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many attempts — wait a few minutes"})
			return
		}
		if !ctEq(req.Code, m.data.Bootstrap) {
			m.bootstrapResult(false)
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "invalid bootstrap code"})
			return
		}
		m.bootstrapResult(true)
	}
	wa, err := m.webauthnFor(r)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	user := m.user(m.rp(r))
	options, session, err := wa.BeginRegistration(user, webauthn.WithExclusions(descriptors(user.creds)))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	label := req.Label
	if label == "" {
		label = "passkey"
	}
	m.putCeremony(w, r, &ceremony{session: *session, rpID: m.rp(r), label: label, bootstrap: !authed})
	writeJSON(w, http.StatusOK, options)
}

func (m *Manager) handleRegisterFinish(w http.ResponseWriter, r *http.Request) {
	cer := m.takeCeremony(r)
	if cer == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no registration in progress"})
		return
	}
	wa, err := m.webauthnFor(r)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	user := m.user(cer.rpID)
	cred, err := wa.FinishRegistration(user, cer.session, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	m.mu.Lock()
	m.data.Creds = append(m.data.Creds, storedCred{
		RPID: cer.rpID, Label: cer.label, CreatedAt: time.Now(), Cred: *cred,
	})
	_ = m.save()
	m.mu.Unlock()
	if cer.bootstrap {
		m.rotateBootstrap()
	}
	m.mintSession(w, r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (m *Manager) handleLoginBegin(w http.ResponseWriter, r *http.Request) {
	user := m.user(m.rp(r))
	if len(user.creds) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no passkeys registered for this host — register one with the bootstrap code"})
		return
	}
	wa, err := m.webauthnFor(r)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	options, session, err := wa.BeginLogin(user)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	m.putCeremony(w, r, &ceremony{session: *session, rpID: m.rp(r)})
	writeJSON(w, http.StatusOK, options)
}

func (m *Manager) handleLoginFinish(w http.ResponseWriter, r *http.Request) {
	cer := m.takeCeremony(r)
	if cer == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no login in progress"})
		return
	}
	wa, err := m.webauthnFor(r)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	user := m.user(cer.rpID)
	cred, err := wa.FinishLogin(user, cer.session, r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	// Persist the updated signature counter for clone detection.
	m.mu.Lock()
	for i := range m.data.Creds {
		if m.data.Creds[i].RPID == cer.rpID && hex.EncodeToString(m.data.Creds[i].Cred.ID) == hex.EncodeToString(cred.ID) {
			m.data.Creds[i].Cred.Authenticator.SignCount = cred.Authenticator.SignCount
			break
		}
	}
	_ = m.save()
	m.mu.Unlock()
	m.mintSession(w, r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (m *Manager) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		m.mu.Lock()
		delete(m.sessions, c.Value)
		m.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (m *Manager) handleListCreds(w http.ResponseWriter, r *http.Request) {
	if !m.Authenticated(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	type item struct {
		ID        string    `json:"id"`
		Label     string    `json:"label"`
		RPID      string    `json:"rpId"`
		CreatedAt time.Time `json:"createdAt"`
	}
	m.mu.Lock()
	out := make([]item, 0, len(m.data.Creds))
	for _, c := range m.data.Creds {
		out = append(out, item{ID: hex.EncodeToString(c.Cred.ID), Label: c.Label, RPID: c.RPID, CreatedAt: c.CreatedAt})
	}
	m.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

func (m *Manager) handleDeleteCred(w http.ResponseWriter, r *http.Request) {
	if !m.Authenticated(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	id := r.PathValue("id")
	m.mu.Lock()
	kept := m.data.Creds[:0]
	for _, c := range m.data.Creds {
		if hex.EncodeToString(c.Cred.ID) != id {
			kept = append(kept, c)
		}
	}
	m.data.Creds = kept
	_ = m.save()
	m.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
