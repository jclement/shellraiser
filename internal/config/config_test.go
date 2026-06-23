package config

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPrecedenceAndCommandMerge(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, ".slopbox.toml", `
addr = ":7000"
postgres = true
[[commands]]
name = "dev"
args = ["shared-dev"]
[[commands]]
name = "test"
args = ["shared-test"]
`)
	write(t, dir, ".slopbox.local.toml", `
addr = ":8000"
[[commands]]
name = "dev"
args = ["local-dev"]
[[commands]]
name = "lint"
args = ["local-lint"]
`)

	t.Setenv("SLOPBOX_ADDR", ":9000")
	t.Setenv("SLOPBOX_POSTGRES", "0")

	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	// env beats local beats shared
	if c.Addr != ":9000" {
		t.Errorf("addr: want :9000 (env), got %q", c.Addr)
	}
	// env disables postgres even though shared enabled it
	if c.PostgresEnabled() {
		t.Error("postgres should be disabled by env")
	}
	// commands: local "dev" overrides shared; "test" kept; "lint" appended
	got := map[string]string{}
	for _, cmd := range c.Commands {
		got[cmd.Name] = cmd.Args[0]
	}
	if got["dev"] != "local-dev" {
		t.Errorf("dev: want local-dev, got %q", got["dev"])
	}
	if got["test"] != "shared-test" {
		t.Errorf("test: want shared-test, got %q", got["test"])
	}
	if got["lint"] != "local-lint" {
		t.Errorf("lint: want local-lint, got %q", got["lint"])
	}
}

func TestDefaults(t *testing.T) {
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != ":7000" {
		t.Errorf("default addr: got %q", c.Addr)
	}
	if c.PostgresEnabled() {
		t.Error("postgres should default OFF in v2 (opt-in)")
	}
	if !c.CodeServerEnabled() {
		t.Error("code-server should default on")
	}
}
