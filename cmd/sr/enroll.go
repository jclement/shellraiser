package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"html/template"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jclement/shellraiser/internal/ui"
	"golang.org/x/crypto/ssh"
)

// Enrollment binds a new device to this backend. The device proves possession of
// its key (a self-signature over its public line), the *authenticated* owner
// approves it in the UI after comparing the fingerprint `sr connect` printed, and
// the device long-polls for the decision — receiving the host-key fingerprint and
// SSH endpoint over the already-authenticated HTTPS channel (closing the TOFU
// gap). Capabilities are chosen here but live on the device (decision #3): the
// backend stores only the pubkey allowlist. See docs/device-link.md.

type pendingEnroll struct {
	code     string
	pubLine  string // authorized_keys line
	fp       string // device-key fingerprint, shown to the owner to compare
	name     string
	created  time.Time
	decided  bool
	approved bool
	caps     []string
	commands []string
	done     chan struct{}
}

type enrollStore struct {
	mu sync.Mutex
	m  map[string]*pendingEnroll
}

func newEnrollStore() *enrollStore { return &enrollStore{m: map[string]*pendingEnroll{}} }

const enrollTTL = 5 * time.Minute

func (s *enrollStore) get(code string) *pendingEnroll {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.m[code]
	if p == nil || time.Since(p.created) > enrollTTL {
		return nil
	}
	return p
}

// capsOffered are the capabilities the enrollment form presents.
var capsOffered = []struct{ Key, Label, Hint string }{
	{capBindPort, "Forward ports", "bind worker ports to this machine's localhost"},
	{capSSHAgent, "SSH agent", "relay this machine's SSH agent into workers (high trust)"},
}

// handleEnrollStart (public): the device registers a pending enrollment, proving
// it holds the private key for the public line it submits.
func (c *Coordinator) handleEnrollStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pubkey string `json:"pubkey"`
		Name   string `json:"name"`
		SigFmt string `json:"sig_format"`
		SigB64 string `json:"sig_blob"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(req.Pubkey))
	if err != nil {
		http.Error(w, "bad pubkey", http.StatusBadRequest)
		return
	}
	blob, err := base64.StdEncoding.DecodeString(req.SigB64)
	if err != nil {
		http.Error(w, "bad signature", http.StatusBadRequest)
		return
	}
	// Possession proof: the device signs its own marshaled public line.
	if err := pub.Verify([]byte(strings.TrimSpace(req.Pubkey)), &ssh.Signature{Format: req.SigFmt, Blob: blob}); err != nil {
		http.Error(w, "possession not proven", http.StatusForbidden)
		return
	}
	p := &pendingEnroll{
		code:    randToken(),
		pubLine: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))),
		fp:      fingerprintSHA256(pub),
		name:    sanitizeName(req.Name),
		created: time.Now(),
		done:    make(chan struct{}),
	}
	c.enroll.mu.Lock()
	c.enroll.m[p.code] = p
	c.enroll.mu.Unlock()
	ui.Info("enroll", "pending device %q (%s) — approve in the UI", p.name, p.fp)
	writeJSON(w, map[string]string{"code": p.code, "fingerprint": p.fp})
}

// handleEnrollStatus (public, long-poll): the device waits for the owner's
// decision and, on approval, receives the host key + SSH endpoint to pin.
func (c *Coordinator) handleEnrollStatus(w http.ResponseWriter, r *http.Request) {
	p := c.enroll.get(r.URL.Query().Get("code"))
	if p == nil {
		http.Error(w, "unknown or expired code", http.StatusNotFound)
		return
	}
	select {
	case <-p.done:
	case <-time.After(25 * time.Second): // long-poll window; device re-polls
	case <-r.Context().Done():
		return
	}
	c.enroll.mu.Lock()
	decided, approved := p.decided, p.approved
	caps := append([]string(nil), p.caps...)
	cmds := append([]string(nil), p.commands...)
	c.enroll.mu.Unlock()
	if !decided {
		writeJSON(w, map[string]any{"status": "pending"})
		return
	}
	if !approved {
		writeJSON(w, map[string]any{"status": "denied"})
		return
	}
	hostFP, sshAddr := "", ""
	if c.devlink != nil {
		hostFP = fingerprintSHA256(c.devlink.hostSigner.PublicKey())
	}
	sshAddr = deviceSSHAddr(r.Host, hostCfg.DeviceLinkAddr)
	writeJSON(w, map[string]any{
		"status":       "approved",
		"name":         p.name,
		"capabilities": caps,
		"commands":     cmds,
		"host_key":     hostFP,
		"ssh_addr":     sshAddr,
	})
}

// handleEnrollPage (gated): the owner's approval form.
func (c *Coordinator) handleEnrollPage(w http.ResponseWriter, r *http.Request) {
	p := c.enroll.get(r.URL.Query().Get("code"))
	if p == nil {
		http.Error(w, "this enrollment request is unknown or has expired", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = enrollTmpl.Execute(w, map[string]any{
		"Code": p.code, "Name": p.name, "FP": p.fp, "Caps": capsOffered,
	})
}

// handleEnrollApprove (gated): the owner approves/denies; on approval the device
// key joins authorized_devices and the chosen capabilities ride back to the
// device via the long-poll.
func (c *Coordinator) handleEnrollApprove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code     string   `json:"code"`
		Name     string   `json:"name"`
		Approve  bool     `json:"approve"`
		Caps     []string `json:"capabilities"`
		Commands []string `json:"commands"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	p := c.enroll.get(req.Code)
	if p == nil {
		http.Error(w, "unknown or expired code", http.StatusNotFound)
		return
	}
	c.enroll.mu.Lock()
	defer c.enroll.mu.Unlock()
	if p.decided {
		http.Error(w, "already decided", http.StatusConflict)
		return
	}
	p.decided = true
	p.approved = req.Approve
	if n := sanitizeName(req.Name); n != "" {
		p.name = n
	}
	if req.Approve {
		p.caps = filterCaps(req.Caps)
		p.commands = cleanCommands(req.Commands)
		hostCfg.AuthorizedDevices = append(hostCfg.AuthorizedDevices, authorizedDevice{
			Name:  p.name,
			Key:   p.pubLine,
			Added: time.Now().UTC().Format("2006-01-02"),
		})
		if err := saveHostConfig(configDir, hostCfg); err != nil {
			http.Error(w, "could not persist", http.StatusInternalServerError)
			return
		}
		ui.Info("enroll", "approved device %q (%s)", p.name, p.fp)
	} else {
		ui.Info("enroll", "denied device %q", p.name)
	}
	close(p.done)
	writeJSON(w, map[string]bool{"ok": true})
}

// deviceSSHAddr combines the host the device reached us on with the device-link
// listener's port, so the device knows where to dial.
func deviceSSHAddr(reqHost, listenAddr string) string {
	host := reqHost
	if h, _, err := net.SplitHostPort(reqHost); err == nil {
		host = h
	}
	_, port, err := net.SplitHostPort(listenAddr)
	if err != nil || port == "" {
		return ""
	}
	return net.JoinHostPort(host, port)
}

func filterCaps(in []string) []string {
	allowed := map[string]bool{capBindPort: true, capSSHAgent: true}
	var out []string
	for _, c := range in {
		if allowed[c] {
			out = append(out, c)
		}
	}
	return out
}

// cleanCommands normalizes the exposed-command list: trim, drop empties, and
// keep only plain tool names (no paths or shell metacharacters).
func cleanCommands(in []string) []string {
	var out []string
	for _, c := range in {
		c = strings.TrimSpace(c)
		if c == "" || strings.ContainsAny(c, "/ \t;&|$`") {
			continue
		}
		out = append(out, c)
	}
	return out
}

func sanitizeName(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}

func randToken() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

var enrollTmpl = template.Must(template.New("enroll").Parse(`<!doctype html>
<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Approve device</title>
<style>body{font:15px system-ui;margin:3rem auto;max-width:30rem;padding:0 1rem}
code{background:#eee;padding:.1em .3em;border-radius:3px}label{display:block;margin:.5rem 0}
button{font:inherit;padding:.5rem 1rem;margin-right:.5rem}.fp{word-break:break-all}</style>
<h2>Approve a new device</h2>
<p>A device wants to link to this backend. <strong>Compare this fingerprint with the
one <code>sr connect</code> printed</strong> before approving:</p>
<p class="fp"><code>{{.FP}}</code></p>
<label>Name <input id="name" value="{{.Name}}"></label>
<fieldset><legend>Capabilities this device will grant</legend>
{{range .Caps}}<label><input type="checkbox" name="cap" value="{{.Key}}"{{if eq .Key "bind_port"}} checked{{end}}> {{.Label}} — <small>{{.Hint}}</small></label>{{end}}
</fieldset>
<label>Exposed CLI tools (comma-separated, e.g. <code>op, gh</code>) — workers run these on this device
<input id="cmds" placeholder="op"></label>
<button onclick="decide(true)">Approve</button><button onclick="decide(false)">Deny</button>
<p id="msg"></p>
<script>
async function decide(ok){
  const caps=[...document.querySelectorAll('input[name=cap]:checked')].map(c=>c.value);
  const cmds=document.getElementById('cmds').value.split(',').map(s=>s.trim()).filter(Boolean);
  const r=await fetch('/enroll/approve',{method:'POST',headers:{'content-type':'application/json'},
    body:JSON.stringify({code:{{.Code}},name:document.getElementById('name').value,approve:ok,capabilities:caps,commands:cmds})});
  document.getElementById('msg').textContent=r.ok?(ok?'Approved — you can return to your device.':'Denied.'):'Error: '+(await r.text());
}
</script>`))
