package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

// coordinatorSigner loads (or generates) the coordinator's single ed25519
// keypair from ~/.config/shellraiser/ssh. The private key (0600) signs SSH connections
// to every worker; the public key is injected into each worker's
// authorized_keys at run, so the coordinator — and only it — can open -L tunnels.
func coordinatorSigner(dir string) (ssh.Signer, string, error) {
	return loadOrCreateSigner(filepath.Join(dir, "ssh", "coordinator_ed25519"))
}

// deviceSigner loads (or generates) this device's ed25519 identity, used to
// authenticate the device link to a backend (its pubkey is what the backend pins
// in config.toml's authorized_devices). Distinct from the coordinator key: one
// host can be both backend and device, and the two identities stay separate.
func deviceSigner(dir string) (ssh.Signer, string, error) {
	return loadOrCreateSigner(filepath.Join(dir, "ssh", "device_ed25519"))
}

// backendHostSigner loads (or generates) the backend's SSH *host* key — the
// server identity a device pins at enrollment (host-key pinning is the link's
// server authentication, see docs/device-link.md). Persisted and stable across
// `sr serve` restarts; distinct from coordinatorSigner (a client key for dialing
// workers).
func backendHostSigner(dir string) (ssh.Signer, error) {
	s, _, err := loadOrCreateSigner(filepath.Join(dir, "ssh", "backend_host_ed25519"))
	return s, err
}

// fingerprintSHA256 is the SHA256 host-key fingerprint a device pins (and the
// backend returns at enrollment), e.g. "SHA256:abc…".
func fingerprintSHA256(pub ssh.PublicKey) string {
	return ssh.FingerprintSHA256(pub)
}

// loadOrCreateSigner returns the ed25519 signer at keyPath, generating and
// persisting a fresh one (0600) plus its .pub when the file is absent. Returns
// the signer and its authorized_keys-format public line.
func loadOrCreateSigner(keyPath string) (ssh.Signer, string, error) {
	if b, err := os.ReadFile(keyPath); err == nil {
		signer, err := ssh.ParsePrivateKey(b)
		if err != nil {
			return nil, "", fmt.Errorf("parse %s: %w", keyPath, err)
		}
		return signer, authorizedLine(signer), nil
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", err
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return nil, "", err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(block), 0o600); err != nil {
		return nil, "", err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, "", err
	}
	_ = os.WriteFile(keyPath+".pub", ssh.MarshalAuthorizedKey(sshPub), 0o600)
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, "", err
	}
	return signer, authorizedLine(signer), nil
}

func authorizedLine(s ssh.Signer) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(s.PublicKey())))
}
