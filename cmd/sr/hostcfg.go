package main

import (
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// hostConfig is the coordinator's global, host-wide configuration, stored at
// ~/.config/shellraiser/config.toml (0600). It is distinct from a project's
// .shellraiser.toml (which describes one worker).
type hostConfig struct {
	// PasswordHash is the bcrypt hash of the login password (managed via the UI).
	PasswordHash string `toml:"password_hash"`
	// SSHPassthrough binds the host SSH agent + ~/.ssh config into every worker so
	// git/ssh "just work" inside the sandbox. Default OFF — it exposes your SSH
	// agent (and any key files) to the untrusted, danger-mode worker.
	SSHPassthrough bool `toml:"ssh_passthrough"`
	// GitPassthrough binds the host ~/.gitconfig (read-only) into every worker.
	GitPassthrough bool `toml:"git_passthrough"`
}

// hostCfg is the loaded global config, set by the daemon at startup and read by
// ensureWorker for the ssh/git passthrough toggles. configDir is where it lives.
var (
	hostCfg   hostConfig
	configDir string
)

func hostConfigPath(dir string) string { return filepath.Join(dir, "config.toml") }

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
