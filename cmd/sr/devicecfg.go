package main

import (
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Capability names — the fixed, net-new vocabulary a device may grant a backend.
// The backend can only *request* these; a device honors one solely if its
// device.toml lists it (see docs/device-link.md). open-URL and copy-to-clipboard
// are deliberately NOT here: they already ship via the in-container browser bridge
// (internal/server/bridge.go), which targets the human's browser directly. Default
// to bind_port only; ssh_agent and op are high-trust opt-ins (signing oracle /
// arbitrary local exec) — see the trust-boundary note in the design doc.
const (
	capBindPort = "bind_port" // forward a worker port to a device-local listener
	capSSHAgent = "ssh_agent" // relay the device's SSH agent into workers (high-trust)
)

// Forwarded commands (e.g. "op", "gh", "aws") are exposed per-backend via the
// Commands list rather than a fixed capability — see deviceBackend.Commands and
// internal/cmdrelay. Each exposed command lets workers run that tool on the
// device, so it's a high-trust grant.

// deviceConfig is the device-side configuration at ~/.config/shellraiser/device.toml.
// It is the source of truth for what this device will do on behalf of a backend:
// the backend requests, the device enforces this file. The device's private key
// lives separately at ssh/device_ed25519 (see deviceSigner).
type deviceConfig struct {
	// Name is this device's human label, offered at enrollment and shown in the
	// backend UI (e.g. "jeff-mac").
	Name string `toml:"name"`
	// Backends is one entry per enrolled backend this device connects to.
	Backends []deviceBackend `toml:"backend"`
}

// deviceBackend is one enrolled backend plus the capabilities this device grants
// it. Capabilities are stored as a list (not a sub-table) so they nest cleanly
// inside a [[backend]] array-of-tables.
type deviceBackend struct {
	// URL is the backend's HTTPS UI address (e.g. https://shellraiser.tailXXXX.ts.net),
	// used for enrollment and as the identity of the backend.
	URL string `toml:"url"`
	// SSHAddr is the backend's device-link SSH endpoint (host:port). Discovered at
	// enrollment; for manual setup, set it alongside the pinned host key.
	SSHAddr string `toml:"ssh_addr"`
	// HostKey pins the backend's SSH host key (SHA256 fingerprint). Captured at
	// enrollment over the authenticated HTTPS channel; a later mismatch aborts.
	HostKey string `toml:"host_key"`
	// Capabilities is the device-authored, device-enforced grant list (cap* above).
	Capabilities []string `toml:"capabilities"`
	// Commands are CLI tools this device runs on behalf of the backend's workers
	// (e.g. "op", "gh", "aws"). Each appears inside containers under the same name
	// and runs here, with the device's local auth. High-trust: the worker can run
	// the tool with any arguments (op gets an extra subcommand policy).
	Commands []string `toml:"commands"`
}

// grants reports whether this backend was granted a capability.
func (b deviceBackend) grants(cap string) bool {
	for _, c := range b.Capabilities {
		if c == cap {
			return true
		}
	}
	return false
}

// backend returns the enrolled entry for a URL, or false if this device has not
// enrolled with it.
func (c *deviceConfig) backend(url string) (deviceBackend, bool) {
	for _, b := range c.Backends {
		if b.URL == url {
			return b, true
		}
	}
	return deviceBackend{}, false
}

func deviceConfigPath(dir string) string { return filepath.Join(dir, "device.toml") }

// loadDeviceConfig reads device.toml (a missing file yields an empty config).
func loadDeviceConfig(dir string) (deviceConfig, error) {
	var c deviceConfig
	b, err := os.ReadFile(deviceConfigPath(dir))
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return c, err
	}
	return c, toml.Unmarshal(b, &c)
}

// saveDeviceConfig writes device.toml atomically with 0600 perms.
func saveDeviceConfig(dir string, c deviceConfig) error {
	path := deviceConfigPath(dir)
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
