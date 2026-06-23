// Package config loads slopbox settings with this precedence (highest first):
//
//	environment variables  →  .slopbox.local.toml  →  .slopbox.toml  →  defaults
//
// Scalars follow that precedence. Custom session `commands` are defined ONLY in
// the .slopbox files (env can't express them); the local file's commands merge
// over the shared file's, matched by name.
package config

import (
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Command is a user-defined session launcher (e.g. "run the dev server").
type Command struct {
	Name string   `toml:"name"`
	Kind string   `toml:"kind"` // session kind label; defaults to "command"
	Icon string   `toml:"icon"`
	Args []string `toml:"args"` // argv to run
}

// Config is the merged runtime configuration.
type Config struct {
	Addr         string `toml:"addr"`
	Name         string `toml:"name"` // repo display name (else derived from git remote)
	WorktreesDir string `toml:"worktrees_dir"`
	NoAuth       bool   `toml:"no_auth"`
	Token        string `toml:"token"`
	RPID         string `toml:"rp_id"`    // pin the WebAuthn RP ID (else discovered from Host)
	Postgres     *bool  `toml:"postgres"` // nil ⇒ default (enabled)
	CodeServer   *bool  `toml:"code"`     // code-server at /edit; nil ⇒ default (enabled)

	// Command overrides for the built-in launchers.
	Shell  []string `toml:"shell"`
	Editor []string `toml:"editor"`
	Claude []string `toml:"claude"`
	Codex  []string `toml:"codex"`

	// Custom launchers (toml-only).
	Commands []Command `toml:"commands"`
}

// PostgresEnabled resolves the tri-state Postgres flag (default on).
func (c Config) PostgresEnabled() bool { return c.Postgres == nil || *c.Postgres }

// CodeServerEnabled resolves the tri-state code-server flag (default on).
func (c Config) CodeServerEnabled() bool { return c.CodeServer == nil || *c.CodeServer }

// Load reads defaults, then the two toml files from repoDir, then env vars.
func Load(repoDir string) (Config, error) {
	c := Config{Addr: ":7000"}
	if err := mergeFile(&c, filepath.Join(repoDir, ".slopbox.toml")); err != nil {
		return c, err
	}
	if err := mergeFile(&c, filepath.Join(repoDir, ".slopbox.local.toml")); err != nil {
		return c, err
	}
	applyEnv(&c)
	return c, nil
}

func mergeFile(c *Config, path string) error {
	var f Config
	md, err := toml.DecodeFile(path, &f)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if md.IsDefined("addr") {
		c.Addr = f.Addr
	}
	if md.IsDefined("name") {
		c.Name = f.Name
	}
	if md.IsDefined("worktrees_dir") {
		c.WorktreesDir = f.WorktreesDir
	}
	if md.IsDefined("no_auth") {
		c.NoAuth = f.NoAuth
	}
	if md.IsDefined("token") {
		c.Token = f.Token
	}
	if md.IsDefined("rp_id") {
		c.RPID = f.RPID
	}
	if md.IsDefined("postgres") {
		c.Postgres = f.Postgres
	}
	if md.IsDefined("code") {
		c.CodeServer = f.CodeServer
	}
	if md.IsDefined("shell") {
		c.Shell = f.Shell
	}
	if md.IsDefined("editor") {
		c.Editor = f.Editor
	}
	if md.IsDefined("claude") {
		c.Claude = f.Claude
	}
	if md.IsDefined("codex") {
		c.Codex = f.Codex
	}
	if md.IsDefined("commands") {
		c.Commands = mergeCommands(c.Commands, f.Commands)
	}
	return nil
}

// mergeCommands overlays overrides onto base, matched by name (override wins),
// preserving base order and appending genuinely new commands.
func mergeCommands(base, overrides []Command) []Command {
	idx := map[string]int{}
	out := make([]Command, len(base))
	copy(out, base)
	for i, cmd := range out {
		idx[cmd.Name] = i
	}
	for _, cmd := range overrides {
		if i, ok := idx[cmd.Name]; ok {
			out[i] = cmd
		} else {
			idx[cmd.Name] = len(out)
			out = append(out, cmd)
		}
	}
	return out
}

func applyEnv(c *Config) {
	if v := os.Getenv("SLOPBOX_ADDR"); v != "" {
		c.Addr = v
	}
	if v := os.Getenv("SLOPBOX_WORKTREES"); v != "" {
		c.WorktreesDir = v
	}
	if v := os.Getenv("SLOPBOX_TOKEN"); v != "" {
		c.Token = v
	}
	if v := os.Getenv("SLOPBOX_RP_ID"); v != "" {
		c.RPID = v
	}
	if v := os.Getenv("SLOPBOX_NO_AUTH"); v != "" {
		c.NoAuth = v == "1" || v == "true"
	}
	if v := os.Getenv("SLOPBOX_POSTGRES"); v != "" {
		b := v == "1" || v == "true"
		c.Postgres = &b
	}
	if v := os.Getenv("SLOPBOX_CODE_SERVER"); v != "" {
		b := v == "1" || v == "true"
		c.CodeServer = &b
	}
}
