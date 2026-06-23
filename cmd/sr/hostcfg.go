package main

import (
	"crypto/rand"
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"github.com/BurntSushi/toml"
)

// hostConfig is the coordinator's global, host-wide configuration, stored at
// ~/.config/shellraiser/config.toml (0600). It is distinct from a project's
// .shellraiser.toml (which describes one worker).
type hostConfig struct {
	// PasswordHash is the bcrypt hash of the login password (managed via the UI).
	PasswordHash string `toml:"password_hash"`
	// Port is the localhost UI port — a stable, large, random port chosen on first
	// run (less guessable / collision-prone than a fixed one). Override with --port.
	Port int `toml:"port"`
	// SSHPassthrough binds the host SSH agent + ~/.ssh config into every worker so
	// git/ssh "just work" inside the sandbox. Default OFF — it exposes your SSH
	// agent (and any key files) to the untrusted, danger-mode worker.
	SSHPassthrough bool `toml:"ssh_passthrough"`
	// GitPassthrough binds the host ~/.gitconfig (read-only) into every worker.
	GitPassthrough bool `toml:"git_passthrough"`
	// SSHAuthSock overrides the host SSH agent socket to forward (else OS default:
	// the Docker Desktop bridge on macOS, $SSH_AUTH_SOCK on Linux). Point it at
	// your 1Password agent socket if needed.
	SSHAuthSock string `toml:"ssh_auth_sock"`
	// Env is injected into every worker (e.g. OP_SERVICE_ACCOUNT_TOKEN so the
	// 1Password CLI `op` works headless, or other account-wide secrets/API keys).
	// These reach the untrusted worker — treat like the shared agent creds.
	Env map[string]string `toml:"env"`
}

// hostCfg is the loaded global config, set by the daemon at startup and read by
// ensureWorker for the ssh/git passthrough toggles. configDir is where it lives.
var (
	hostCfg   hostConfig
	configDir string
)

func hostConfigPath(dir string) string { return filepath.Join(dir, "config.toml") }

// resolveUIPort returns the persisted localhost UI port, choosing a stable random
// high port on first run and saving it. Falls back to 7700 only if it can't pick
// or persist one.
func resolveUIPort(dir string) string {
	c, _ := loadHostConfig(dir)
	if c.Port != 0 {
		return strconv.Itoa(c.Port)
	}
	p := randomHighPort()
	c.Port = p
	_ = saveHostConfig(dir, c)
	return strconv.Itoa(p)
}

// randomHighPort returns a free TCP port in the 20000–60000 range (above the
// common service range, below the typical ephemeral range).
func randomHighPort() int {
	for i := 0; i < 50; i++ {
		var b [2]byte
		_, _ = rand.Read(b[:])
		p := 20000 + int(binary.BigEndian.Uint16(b[:]))%40000
		ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
		if err == nil {
			ln.Close()
			return p
		}
	}
	return 7700
}

// loadHostConfig reads the global config (a missing file yields defaults).
func loadHostConfig(dir string) (hostConfig, error) {
	var c hostConfig
	b, err := os.ReadFile(hostConfigPath(dir))
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return c, err
	}
	return c, toml.Unmarshal(b, &c)
}

// saveHostConfig writes the global config atomically with 0600 perms (it holds
// the password hash).
func saveHostConfig(dir string, c hostConfig) error {
	path := hostConfigPath(dir)
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := toml.NewEncoder(f).Encode(c); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
