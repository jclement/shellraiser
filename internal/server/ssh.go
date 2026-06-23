package server

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// authKeysMu guards concurrent edits to ~/.ssh/authorized_keys.
var authKeysMu sync.Mutex

const ephemeralTTL = 2 * time.Minute

// handleSSHCommand mints a short-lived ephemeral SSH keypair, authorizes it for
// `ephemeralTTL`, and returns a self-contained `ssh` command (the private key
// inline) that forwards every running dev port — a one-click "reach my box".
func (s *Server) handleSSHCommand(w http.ResponseWriter, r *http.Request) {
	if os.Getenv("SLOPBOX_SSH") != "1" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("ssh is not enabled — start the box with --ssh"))
		return
	}
	home := os.Getenv("HOME")
	if home == "" {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("no HOME"))
		return
	}

	// Mint an ephemeral ed25519 key with a unique marker comment.
	marker := "slopbox-ephemeral-" + hexN(8)
	dir, err := os.MkdirTemp("", "slopssh")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer os.RemoveAll(dir)
	keyPath := filepath.Join(dir, "k")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", marker, "-f", keyPath).CombinedOutput(); err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("ssh-keygen: %s", strings.TrimSpace(string(out))))
		return
	}
	priv, err := os.ReadFile(keyPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	pub, err := os.ReadFile(keyPath + ".pub")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	akPath := filepath.Join(home, ".ssh", "authorized_keys")
	if err := appendAuthorizedKey(akPath, strings.TrimSpace(string(pub))); err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("authorize key: %w", err))
		return
	}
	// Time-limited: remove the key after the window (an established session that
	// connected in time keeps running).
	time.AfterFunc(ephemeralTTL, func() { _ = removeAuthorizedKeyByMarker(akPath, marker) })

	// Forward every listening dev port (skip slopbox's own internals).
	hostName := hostOnly(r.Host)
	sshPort := "22"
	if isLocal(hostName) {
		sshPort = envOr2("SLOPBOX_SSH_HOST_PORT", "2222") // published host port
	}
	ports := devPorts(s.cfg.Addr)
	var fwd strings.Builder
	for _, p := range ports {
		fmt.Fprintf(&fwd, " -L %d:localhost:%d", p, p)
	}

	cmd := fmt.Sprintf(`KEY="$(mktemp)"; cat > "$KEY" <<'SLOPKEY'
%s
SLOPKEY
chmod 600 "$KEY"; ssh -i "$KEY" -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=/dev/null -p %s%s ubuntu@%s; rm -f "$KEY"`,
		strings.TrimRight(string(priv), "\n"), sshPort, fwd.String(), hostName)

	writeJSON(w, map[string]any{"command": cmd, "ttlSeconds": int(ephemeralTTL.Seconds()), "ports": ports, "host": hostName, "port": sshPort})
}

// devPorts returns listening TCP ports minus slopbox's own internals.
func devPorts(addr string) []int {
	skip := map[int]bool{8081: true, 8082: true, 22: true} // pgweb, code-server, sshd
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		if n, err := strconv.Atoi(addr[i+1:]); err == nil {
			skip[n] = true
		}
	}
	var out []int
	for _, p := range listeningPorts() {
		if p.Port > 0 && !skip[p.Port] {
			out = append(out, p.Port)
		}
	}
	return out
}

func appendAuthorizedKey(path, line string) error {
	authKeysMu.Lock()
	defer authKeysMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(line + "\n")
	return err
}

func removeAuthorizedKeyByMarker(path, marker string) error {
	authKeysMu.Lock()
	defer authKeysMu.Unlock()
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var kept []string
	for _, ln := range strings.Split(string(b), "\n") {
		if ln != "" && !strings.Contains(ln, marker) {
			kept = append(kept, ln)
		}
	}
	out := strings.Join(kept, "\n")
	if out != "" {
		out += "\n"
	}
	return os.WriteFile(path, []byte(out), 0o600)
}

func hostOnly(h string) string {
	if i := strings.LastIndex(h, ":"); i >= 0 {
		return h[:i]
	}
	return h
}

func isLocal(h string) bool { return h == "localhost" || h == "127.0.0.1" || h == "::1" }

func envOr2(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func hexN(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
