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
// keypair from ~/.config/sbox/ssh. The private key (0600) signs SSH connections
// to every worker; the public key is injected into each worker's
// authorized_keys at run, so the coordinator — and only it — can open -L tunnels.
func coordinatorSigner(dir string) (ssh.Signer, string, error) {
	keyPath := filepath.Join(dir, "ssh", "coordinator_ed25519")
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
