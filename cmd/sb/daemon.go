package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/jclement/slopbox/internal/auth"
	"github.com/jclement/slopbox/internal/ui"
)

const version = "0.2.0"

// coordMeta is written by the daemon so clients can find it.
type coordMeta struct {
	PID  int    `json:"pid"`
	Port string `json:"port"`
	Sock string `json:"sock"`
}

func metaPath(dir string) string { return filepath.Join(dir, "coord.json") }
func lockPath(dir string) string { return filepath.Join(dir, "coord.lock") }
func sockPath(dir string) string { return filepath.Join(dir, "sb.sock") }

// sockClient returns an HTTP client bound to the daemon's unix control socket.
func sockClient(sock string) *http.Client {
	return &http.Client{
		Timeout: 90 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sock)
			},
		},
	}
}

// liveCoordinator returns the running daemon's meta if one answers /health.
func liveCoordinator(dir string) (*coordMeta, bool) {
	b, err := os.ReadFile(metaPath(dir))
	if err != nil {
		return nil, false
	}
	var m coordMeta
	if json.Unmarshal(b, &m) != nil || m.Sock == "" {
		return nil, false
	}
	resp, err := sockClient(m.Sock).Get("http://unix/health")
	if err != nil {
		return nil, false
	}
	resp.Body.Close()
	return &m, resp.StatusCode == 200
}

// runDaemon is the detached coordinator process (sb __daemon). It single-
// instances via an exclusive flock, serves the UI + control socket, and exits if
// another daemon already holds the lock.
func runDaemon(dir, port string, noAuth bool) {
	lf, err := os.OpenFile(lockPath(dir), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		fatal("daemon lock: %v", err)
	}
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		ui.Info("sb", "another coordinator is already running — exiting")
		os.Exit(0)
	}
	// We own the lock for our lifetime.
	_ = os.WriteFile(metaPath(dir), mustJSON(coordMeta{PID: os.Getpid(), Port: port, Sock: sockPath(dir)}), 0o600)

	am, err := auth.New(filepath.Join(dir, "auth", "store.json"), "", "", noAuth)
	if err != nil {
		fatal("auth: %v", err)
	}
	am.Logf = func(format string, a ...any) { ui.Info("auth", format, a...) }

	// Coordinator SSH key: signs tunnels to workers; its pubkey is injected into
	// each worker (so only we can -L through their sshd).
	signer, authKey, err := coordinatorSigner(dir)
	if err != nil {
		fatal("ssh key: %v", err)
	}
	coordAuthKey = authKey

	co := newCoordinator(port, am)
	co.pm = newPortMapper(signer)
	co.reg.reconcile() // re-adopt any workers from a previous run
	if err := co.Run(sockPath(dir)); err != nil {
		fatal("%v", err)
	}
}

// ensureCoordinator returns the meta of a running daemon, spawning a detached one
// if none is alive. The first sb in any shell starts the daemon; the rest attach.
func ensureCoordinator(dir, port string, noAuth bool) (*coordMeta, error) {
	if m, ok := liveCoordinator(dir); ok {
		return m, nil
	}
	if err := spawnDaemon(dir, port, noAuth); err != nil {
		return nil, err
	}
	// Wait for it to listen.
	for i := 0; i < 80; i++ {
		if m, ok := liveCoordinator(dir); ok {
			return m, nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	return nil, fmt.Errorf("coordinator did not come up — see %s", filepath.Join(dir, "coordinator.log"))
}

// spawnDaemon re-execs this binary as a detached background coordinator
// (Setsid + stdio to a log file), so it outlives the launching shell.
func spawnDaemon(dir, port string, noAuth bool) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	args := []string{"__daemon", "--port", port}
	if noAuth {
		args = append(args, "--no-auth")
	}
	logf, err := os.OpenFile(filepath.Join(dir, "coordinator.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	cmd := exec.Command(self, args...)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.Env = append(os.Environ(), "SBOX_HOME="+dir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Process.Release()
	return nil
}

// registerWithDetails asks the daemon to ensure a worker for project, prints the
// first-run bootstrap code if auth needs enrolling, and returns the worker id.
func registerWithDetails(m *coordMeta, project string) (string, error) {
	body := mustJSON(map[string]string{"project": project})
	resp, err := sockClient(m.Sock).Post("http://unix/register", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := readAll(resp)
		return "", fmt.Errorf("register failed: %s", b)
	}
	var out struct {
		ID          string `json:"id"`
		AuthEnabled bool   `json:"authEnabled"`
		Registered  bool   `json:"registered"`
		Bootstrap   string `json:"bootstrap"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.AuthEnabled && !out.Registered && out.Bootstrap != "" {
		ui.Info("auth", "first run — register a passkey with bootstrap code: %s", out.Bootstrap)
	}
	return out.ID, nil
}

// openBrowser best-effort opens a URL in the default browser (SB_NO_OPEN skips).
func openBrowser(url string) {
	if os.Getenv("SB_NO_OPEN") != "" {
		return
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func readAll(resp *http.Response) (string, error) {
	buf := new(bytes.Buffer)
	_, err := buf.ReadFrom(resp.Body)
	return buf.String(), err
}
