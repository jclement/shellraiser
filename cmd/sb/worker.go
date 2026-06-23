package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/jclement/slopbox/internal/config"
)

// Worker is one project's backend container, fronted by the coordinator.
type Worker struct {
	ID        string // sb container/volume identity
	Project   string // absolute path of the project (git repo) on the host
	Name      string // display name (repo name; falls back to ID)
	Container string // sb_<id>
	Network   string // sb_net_<id>
	Volume    string // sb_<id>_vol
	APIPort   string // loopback host port → container :7000
	SSHPort   string // loopback host port → container :22 (Phase 3)
	Token     string // SLOPBOX_WORKER_TOKEN injected at run; required on every proxied hop
	State     string // docker State.Status: running | exited | …
}

// coordAuthKey is the coordinator's authorized_keys line, set by the daemon at
// startup and injected into each worker so the port-mapper can SSH in.
var coordAuthKey string

// agentsVolume holds the shared claude/codex credential files (written only by
// `sb login`, read-only into every non-isolated worker).
const agentsVolume = "sbox_agents"

func ensureAgentsVolume() {
	if exec.Command("docker", "volume", "inspect", agentsVolume).Run() != nil {
		_, _ = dockerRun("volume", "create", agentsVolume)
	}
}

func containerName(id string) string { return "sb_" + id }
func networkName(id string) string   { return "sb_net_" + id }
func volumeName(id string) string    { return "sb_" + id + "_vol" }

var idRe = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

// boxID resolves a project's stable identity: an explicit `id` in .slopbox.toml,
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
// in .slopbox.toml — enough to read `id` before the full config loader exists on
// the host side. (The worker still parses the file authoritatively.)
func tomlScalar(project, key string) string {
	for _, f := range []string{".slopbox.toml", ".slopbox.local.toml"} {
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

// declaredPorts resolves the project's .slopbox.toml `ports` (single ports and
// "a-b" ranges) into a flat list to auto-map. Ranges are capped to keep a typo
// from spawning thousands of listeners.
func declaredPorts(project string) []int {
	cfg, err := config.Load(project)
	if err != nil {
		return nil
	}
	var out []int
	for _, spec := range cfg.Ports {
		spec = strings.TrimSpace(spec)
		if lo, hi, ok := strings.Cut(spec, "-"); ok {
			a, e1 := strconv.Atoi(strings.TrimSpace(lo))
			b, e2 := strconv.Atoi(strings.TrimSpace(hi))
			if e1 != nil || e2 != nil || b < a || b-a > 64 {
				continue
			}
			for p := a; p <= b; p++ {
				out = append(out, p)
			}
			continue
		}
		if p, err := strconv.Atoi(spec); err == nil {
			out = append(out, p)
		}
	}
	return out
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
		w.Token = containerEnv(w.Container, "SLOPBOX_WORKER_TOKEN")
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
		"--label", "slopbox.managed=1",
		"--label", "slopbox.id=" + id,
		"--label", "slopbox.project=" + project,
		"--label", "slopbox.role=worker",
		"-v", project + ":/work",
		"-v", w.Volume + ":/home/ubuntu",
		"-p", "127.0.0.1:0:7000", // API — loopback, ephemeral host port
		"-p", "127.0.0.1:0:22", // sshd — loopback, ephemeral host port (Phase 3)
		"--memory", "2g", "--memory-swap", "2g",
		"--pids-limit", "512",
		"--cpu-shares", "1024",
		"-e", "SLOPBOX_REPO=/work",
		"-e", "SLOP_ID=" + id,
		"-e", "SLOPBOX_WORKER_TOKEN=" + w.Token,
		"-e", "SLOPBOX_SSH=1",
		"-e", "SLOPBOX_NO_AUTH=1", // coordinator owns passkey auth; token fences the port
	}
	if coordAuthKey != "" {
		// The coordinator's pubkey → the worker's authorized_keys, so only the
		// coordinator can open -L tunnels through the worker's sshd.
		args = append(args, "-e", "SLOPBOX_SSH_PUBKEY="+coordAuthKey)
	}
	// Tell the entrypoint whether to start postgres (default off), matching what
	// the worker's own config resolves so the process and the /db tab agree.
	cfg, _ := config.Load(project)
	if cfg.PostgresEnabled() {
		args = append(args, "-e", "SLOPBOX_POSTGRES=1")
	}

	// Agent logins. Hot, per-interaction state (~/.claude.json, sessions) is
	// relocated per-worker into the home volume via CLAUDE_CONFIG_DIR/CODEX_HOME
	// so concurrent workers never corrupt the monolithic .claude.json. Unless the
	// project opts out, the shared creds volume is mounted READ-ONLY at /agents
	// and the entrypoint seeds credential files from it (single-writer: sb login).
	args = append(args,
		"-e", "CLAUDE_CONFIG_DIR=/home/ubuntu/.config/claude",
		"-e", "CODEX_HOME=/home/ubuntu/.config/codex",
	)
	if !cfg.IsolatedAgents {
		ensureAgentsVolume()
		args = append(args, "-v", agentsVolume+":/agents:ro")
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
