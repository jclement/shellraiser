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
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/jclement/shellraiser/internal/auth"
	"github.com/jclement/shellraiser/internal/ui"
	"tailscale.com/tsnet"
)

// version is the build version — a git describe (e.g. "a1b2c3d" or
// "a1b2c3d-dirty") injected via -ldflags "-X main.version=…" by mise/goreleaser;
// "dev" for a plain `go build`. Also gates the base image tag.
var version = "dev"

// coordMeta is written by the daemon so clients can find it.
type coordMeta struct {
	PID  int    `json:"pid"`
	Port string `json:"port"`
	Sock string `json:"sock"`
}

func metaPath(dir string) string { return filepath.Join(dir, "coord.json") }
func lockPath(dir string) string { return filepath.Join(dir, "coord.lock") }
func sockPath(dir string) string { return filepath.Join(dir, "sr.sock") }

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

// runDaemon is the coordinator process. Normally it's the detached daemon (sr
// __daemon); with initProject set it's the foreground `sr --fg` dev mode, which
// also registers that project and tears it down on Ctrl-C. It single-instances
// via an exclusive flock and exits if another coordinator already holds it.
func runDaemon(dir, port string, noAuth, tailnet bool, initProject, initImage string) {
	lf, err := os.OpenFile(lockPath(dir), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		fatal("daemon lock: %v", err)
	}
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		ui.Info("sr", "another coordinator is already running — exiting")
		os.Exit(0)
	}
	// We own the lock for our lifetime.
	_ = os.WriteFile(metaPath(dir), mustJSON(coordMeta{PID: os.Getpid(), Port: port, Sock: sockPath(dir)}), 0o600)

	// Global config holds the password hash + passthrough toggles. One source of
	// truth (the package global) so auth and the /api/config endpoint agree.
	if hostCfg, err = loadHostConfig(dir); err != nil {
		fatal("config: %v", err)
	}
	configDir = dir
	am := auth.New(hostCfg.PasswordHash, func(hash string) error {
		hostCfg.PasswordHash = hash
		return saveHostConfig(dir, hostCfg)
	}, noAuth)
	am.Logf = func(format string, a ...any) { ui.Info("auth", format, a...) }

	// Coordinator SSH key: signs tunnels to workers; its pubkey is injected into
	// each worker (so only we can -L through their sshd).
	signer, authKey, err := coordinatorSigner(dir)
	if err != nil {
		fatal("ssh key: %v", err)
	}
	coordAuthKey = authKey

	// One tsnet node, shared by the UI listener and the port-mapper (so mapped
	// ports can also bind the tailnet IP).
	var ts *tsnet.Server
	if tailnet {
		ts = newTailnetServer(dir)
		defer ts.Close()
	}

	var tl tailnetListener // true-nil interface when tailnet is off
	if ts != nil {
		tl = ts
	}
	co := newCoordinator(port, am)
	co.dev = localDevice{}
	co.pm = newPortMapper(signer, co.dev, tl)
	co.ports = newPortStore(dir)
	startDeviceLink(co, dir) // device-link SSH server (no-op unless device_link_addr is set)
	co.reg.reconcile()       // re-adopt any workers from a previous run
	for _, w := range co.reg.list() {
		if w.State == "running" && w.APIPort != "" {
			co.onWorkerUp(w) // re-forward ports + relay the agent for survivors
		}
	}
	if ts != nil {
		go serveTailnetUI(co, ts)
	}

	// Foreground dev mode: register this project now and stop it on Ctrl-C.
	if initProject != "" {
		id := boxID(initProject)
		w, werr := provisionWorker(id, initProject, initImage)
		if werr != nil {
			fatal("%v", werr)
		}
		if !w.BareMetal {
			waitReady(w)
			co.onWorkerUp(w)
		}
		co.reg.put(w)
		co.act.touch(id)
		url := fmt.Sprintf("http://127.0.0.1:%s/w/%s/", port, id)
		ui.Info("sr", "project %q ready (%s)", id, workerKind(w))
		_ = co.dev.OpenURL(url)
		go func() {
			sig := make(chan os.Signal, 2)
			signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
			<-sig
			ui.Info("sr", "stopping %s… (Ctrl-C again to force-quit)", id)
			// Watchdog: don't let a hung teardown/docker-stop trap the process.
			// A second signal, or a 15s deadline, forces an immediate exit.
			go func() {
				select {
				case <-sig:
					ui.Warn("sr", "forced shutdown")
				case <-time.After(15 * time.Second):
					ui.Warn("sr", "shutdown timed out — exiting (worker may still be stopping)")
				}
				os.Exit(1)
			}()
			runTeardown(w)
			if w.BareMetal {
				if w.srv != nil {
					w.srv.Shutdown()
				}
			} else {
				co.pm.CloseWorker(id)
				_, _ = dockerRun("stop", w.Container)
			}
			os.Exit(0)
		}()
	}

	if err := co.Run(sockPath(dir)); err != nil {
		fatal("%v", err)
	}
}

// ensureCoordinator returns the meta of a running daemon, spawning a detached one
// if none is alive. The first sr in any shell starts the daemon; the rest attach.
func ensureCoordinator(dir, port string, noAuth, tailnet bool) (*coordMeta, error) {
	if m, ok := liveCoordinator(dir); ok {
		return m, nil
	}
	if err := spawnDaemon(dir, port, noAuth, tailnet); err != nil {
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
func spawnDaemon(dir, port string, noAuth, tailnet bool) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	args := []string{"__daemon", "--port", port}
	if noAuth {
		args = append(args, "--no-auth")
	}
	if tailnet {
		args = append(args, "--tailnet")
	}
	logf, err := os.OpenFile(filepath.Join(dir, "coordinator.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	cmd := exec.Command(self, args...)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.Env = append(os.Environ(), "SHELLRAISER_HOME="+dir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Process.Release()
	return nil
}

// registerWithDetails asks the daemon to ensure a worker for project (using a
// pre-built image), prints the first-run one-time password if needed, and
// returns the worker id.
func registerWithDetails(m *coordMeta, project, image string) (string, error) {
	body := mustJSON(map[string]string{"project": project, "image": image})
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
		ID           string `json:"id"`
		AuthEnabled  bool   `json:"authEnabled"`
		HasPassword  bool   `json:"hasPassword"`
		TempPassword string `json:"tempPassword"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.AuthEnabled && !out.HasPassword && out.TempPassword != "" {
		ui.Info("auth", "first run — sign in with one-time password: %s", out.TempPassword)
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
