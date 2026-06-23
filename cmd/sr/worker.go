package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/jclement/shellraiser/internal/config"
	"github.com/jclement/shellraiser/internal/server"
	"github.com/jclement/shellraiser/internal/ui"
)

// Worker is one project's backend, fronted by the coordinator. Normally a
// container; with BareMetal it's an in-process server running on the host.
type Worker struct {
	ID        string // sr container/volume identity
	Project   string // absolute path of the project (git repo) on the host
	Name      string // display name (repo name; falls back to ID)
	Container string // sr_<id>
	Network   string // sr_net_<id>
	Volume    string // sr_<id>_vol
	APIPort   string // loopback host port → container :7000
	SSHPort   string // loopback host port → container :22 (Phase 3)
	Token     string // SHELLRAISER_WORKER_TOKEN injected at run; required on every proxied hop
	State     string // docker State.Status: running | exited | …

	// Bare-metal (no container): the worker runs in the coordinator process,
	// serving handler directly against the project on disk.
	BareMetal bool
	handler   http.Handler
	srv       *server.Server
}

// provisionWorker creates a worker for project — bare-metal (in-process) when the
// project opts in, else a hardened container.
func provisionWorker(id, project, image string) (*Worker, error) {
	cfg, _ := config.Load(project)
	if cfg.BareMetal {
		return newBareWorker(id, project, cfg)
	}
	return ensureWorker(id, project, image)
}

// newBareWorker runs the worker in-process on the host — no container, no
// isolation. Sessions are host processes in the real project dir; dev servers
// bind host localhost (so /p/ and a plain localhost:<port> both work, no SSH
// tunnel needed).
func newBareWorker(id, project string, cfg config.Config) (*Worker, error) {
	srv, err := server.New(project, cfg)
	if err != nil {
		return nil, err
	}
	return &Worker{
		ID:        id,
		Project:   project,
		Name:      filepath.Base(project),
		State:     "running",
		BareMetal: true,
		srv:       srv,
		handler:   srv.Handler(),
	}, nil
}

// coordAuthKey is the coordinator's authorized_keys line, set by the daemon at
// startup and injected into each worker so the port-mapper can SSH in.
var coordAuthKey string

// agentsVolume holds the shared claude/codex credential files (written only by
// `sr login`, read-only into every non-isolated worker).
const agentsVolume = "shellraiser_agents"

func ensureAgentsVolume() {
	if exec.Command("docker", "volume", "inspect", agentsVolume).Run() != nil {
		_, _ = dockerRun("volume", "create", agentsVolume)
	}
}

// runTeardown executes the project's `teardown` command before the workspace is
// stopped/nuked (container: as ubuntu in /work, with a timeout; bare-metal: on
// the host in the project dir). Best-effort.
func runTeardown(w *Worker) {
	cfg, _ := config.Load(w.Project)
	if len(cfg.Teardown) == 0 {
		return
	}
	ui.Info("sr", "teardown %s: %s", w.ID, strings.Join(cfg.Teardown, " "))
	if w.BareMetal {
		cmd := exec.Command(cfg.Teardown[0], cfg.Teardown[1:]...)
		cmd.Dir = w.Project
		_ = cmd.Run()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	args := append([]string{"exec", "-u", "ubuntu", "-w", "/work", w.Container}, cfg.Teardown...)
	_ = exec.CommandContext(ctx, "docker", args...).Run()
}

func workerKind(w *Worker) string {
	if w.BareMetal {
		return "bare metal"
	}
	return "container " + w.Container
}

// isBareMetal reports whether a project opts out of containers.
func isBareMetal(project string) bool {
	cfg, _ := config.Load(project)
	return cfg.BareMetal
}

// sshGitMounts returns the docker -v/-e args for SSH/git passthrough, honoring
// the global config toggles. The SSH agent socket is engine-aware: on a Docker
// Desktop / OrbStack VM engine (macOS/Windows) the host agent is bridged at
// /run/host-services/ssh-auth.sock; on a native Linux engine it's $SSH_AUTH_SOCK.
func sshGitMounts() []string {
	var out []string
	home, _ := os.UserHomeDir()
	if hostCfg.SSHPassthrough {
		if sock := agentSocket(); sock != "" {
			out = append(out, "-v", sock+":/ssh-agent", "-e", "SSH_AUTH_SOCK=/ssh-agent")
		}
		if home != "" {
			if st, err := os.Stat(filepath.Join(home, ".ssh")); err == nil && st.IsDir() {
				out = append(out, "-v", filepath.Join(home, ".ssh")+":/ssh-host:ro")
			}
		}
	}
	if hostCfg.GitPassthrough && home != "" {
		if gc := filepath.Join(home, ".gitconfig"); fileExists(gc) {
			out = append(out, "-v", gc+":/home/ubuntu/.gitconfig:ro")
		}
	}
	return out
}

// agentSocket resolves the host SSH agent socket path to bind into the worker.
func agentSocket() string {
	if hostCfg.SSHAuthSock != "" {
		return hostCfg.SSHAuthSock // explicit override (e.g. the 1Password agent)
	}
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		// The docker engine runs in a VM; Docker Desktop/OrbStack bridge the host
		// agent (whatever the host SSH_AUTH_SOCK points at — incl. 1Password) to
		// this well-known in-VM path.
		return "/run/host-services/ssh-auth.sock"
	}
	return os.Getenv("SSH_AUTH_SOCK") // native Linux engine
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func containerName(id string) string { return "sr_" + id }
func networkName(id string) string   { return "sr_net_" + id }
func volumeName(id string) string    { return "sr_" + id + "_vol" }

var idRe = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

// boxID resolves a project's stable identity: an explicit `id` in .shellraiser.toml,
// else the sanitized folder basename. (Basename can collide across same-named
// repos; the coordinator de-dupes against the registry and the user can pin an
// id in config — a path hash is the documented fallback.)
func boxID(project string) string {
	if v := tomlScalar(project, "id"); v != "" {
		return idRe.ReplaceAllString(v, "-")
	}
	base := idRe.ReplaceAllString(filepath.Base(project), "-")
	if base == "" {
		base = "project"
	}
	return base
}

// tomlScalar does a deliberately tiny line-scan for a top-level `key = "value"`
// in .shellraiser.toml — enough to read `id` before the full config loader exists on
// the host side. (The worker still parses the file authoritatively.)
func tomlScalar(project, key string) string {
	for _, f := range []string{".shellraiser.toml", ".shellraiser.local.toml"} {
		b, err := os.ReadFile(filepath.Join(project, f))
		if err != nil {
			continue
		}
		for _, ln := range strings.Split(string(b), "\n") {
			ln = strings.TrimSpace(ln)
			if strings.HasPrefix(ln, "[") { // entered a table; stop scanning top-level
				break
			}
			if k, v, ok := strings.Cut(ln, "="); ok && strings.TrimSpace(k) == key {
				return strings.Trim(strings.TrimSpace(v), `"' `)
			}
		}
	}
	return ""
}

// declaredPorts returns the project's named [[ports]] mappings to forward on start.
func declaredPorts(project string) []config.PortMap {
	cfg, err := config.Load(project)
	if err != nil {
		return nil
	}
	return cfg.Ports
}

func newToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ensureWorker starts (or re-adopts) a hardened worker container for project and
// returns it populated with its live loopback ports. Hardening vs v1's bare run:
// a per-worker docker network, resource caps, a loopback-published sshd, and a
// coordinator-injected API token. The worker's own passkey auth is always off —
// auth lives at the coordinator now; the token fences the loopback port.
func ensureWorker(id, project, image string) (*Worker, error) {
	w := &Worker{
		ID:        id,
		Project:   project,
		Name:      filepath.Base(project),
		Container: containerName(id),
		Network:   networkName(id),
		Volume:    volumeName(id),
	}

	if containerState(w.Container) == "running" {
		// Re-adopt: reuse the live container and read back its token + ports.
		w.Token = containerEnv(w.Container, "SHELLRAISER_WORKER_TOKEN")
		return populatePorts(w)
	}
	_, _ = dockerRun("rm", "-f", w.Container) // clear any stopped remnant

	// Image is pre-built by the client (so build progress streams to the user's
	// terminal); fall back to resolving here if a caller didn't supply it.
	if image == "" {
		var err error
		if image, err = resolveImage(project); err != nil {
			return nil, err
		}
	}

	if err := ensureNetwork(w.Network); err != nil {
		return nil, err
	}
	w.Token = newToken()

	args := []string{
		"run", "-d", "--name", w.Container,
		"--network", w.Network,
		"--label", "shellraiser.managed=1",
		"--label", "shellraiser.id=" + id,
		"--label", "shellraiser.project=" + project,
		"--label", "shellraiser.role=worker",
		"-v", project + ":/work",
		"-v", w.Volume + ":/home/ubuntu",
		"-p", "127.0.0.1:0:7000", // API — loopback, ephemeral host port
		"-p", "127.0.0.1:0:22", // sshd — loopback, ephemeral host port (Phase 3)
		"--memory", "2g", "--memory-swap", "2g",
		"--pids-limit", "512",
		"--cpu-shares", "1024",
		"-e", "SHELLRAISER_REPO=/work",
		"-e", "SHELLRAISER_ID=" + id,
		"-e", "SHELLRAISER_NAME=" + filepath.Base(project), // real project name (mount basename is just "work")
		"-e", "SHELLRAISER_WORKER_TOKEN=" + w.Token,
		"-e", "SHELLRAISER_SSH=1",
		"-e", "SHELLRAISER_NO_AUTH=1", // coordinator owns passkey auth; token fences the port
	}
	if coordAuthKey != "" {
		// The coordinator's pubkey → the worker's authorized_keys, so only the
		// coordinator can open -L tunnels through the worker's sshd.
		args = append(args, "-e", "SHELLRAISER_SSH_PUBKEY="+coordAuthKey)
	}
	// Tell the entrypoint whether to start postgres (default off), matching what
	// the worker's own config resolves so the process and the /db tab agree.
	cfg, _ := config.Load(project)
	if cfg.PostgresEnabled() {
		args = append(args, "-e", "SHELLRAISER_POSTGRES=1")
	}

	// Agent logins. Hot, per-interaction state (~/.claude.json, sessions) is
	// relocated per-worker into the home volume via CLAUDE_CONFIG_DIR/CODEX_HOME
	// so concurrent workers never corrupt the monolithic .claude.json. Unless the
	// project opts out, the shared creds volume is mounted READ-ONLY at /agents
	// and the entrypoint seeds credential files from it (single-writer: sr login).
	args = append(args,
		"-e", "CLAUDE_CONFIG_DIR=/home/ubuntu/.config/claude",
		"-e", "CODEX_HOME=/home/ubuntu/.config/codex",
	)
	if !cfg.IsolatedAgents {
		ensureAgentsVolume()
		args = append(args, "-v", agentsVolume+":/agents:ro")
	}

	// SSH/git passthrough (global opt-in): forward the host SSH agent (YubiKey
	// included) + bind ~/.ssh config and ~/.gitconfig so git/ssh "just work"
	// inside the sandbox. Off by default — it exposes your agent/keys to the
	// untrusted worker.
	args = append(args, sshGitMounts()...)

	// Global env injection (e.g. OP_SERVICE_ACCOUNT_TOKEN for the 1Password CLI).
	for k, v := range hostCfg.Env {
		args = append(args, "-e", k+"="+v)
	}

	args = append(args, image)
	if _, err := dockerRun(args...); err != nil {
		return nil, fmt.Errorf("start worker: %w", err)
	}
	return populatePorts(w)
}

func populatePorts(w *Worker) (*Worker, error) {
	api, err := publishedPort(w.Container, "7000")
	if err != nil {
		return nil, err
	}
	w.APIPort = api
	w.SSHPort, _ = publishedPort(w.Container, "22") // best-effort; Phase 3 needs it
	w.State = containerState(w.Container)
	return w, nil
}
