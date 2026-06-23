package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// globalDir resolves the coordinator's state directory and ensures it exists
// with 0700 perms. Precedence: $SBOX_HOME → os.UserConfigDir()/sbox
// (~/.config/sbox on Linux, ~/Library/Application Support/sbox on macOS).
//
// The directory concentrates every secret (passkey store, SSH key, tsnet state,
// per-project env files, the worker registry), so it must never be group/world
// accessible — the coordinator refuses to run if it is.
func globalDir() (string, error) {
	dir := os.Getenv("SBOX_HOME")
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("resolve config dir: %w", err)
		}
		dir = filepath.Join(base, "sbox")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	// Tighten perms if a pre-existing dir is too open (ssh-style refusal).
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	for _, sub := range []string{"ssh", "secrets", "workers", "tsnet", "auth", "image"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			return "", err
		}
	}
	return dir, nil
}
