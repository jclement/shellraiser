// Command shellraiser serves the web UI for a single sandboxed vibe-coding box.
package main

import (
	"flag"
	"log"
	"os"
	"path/filepath"

	"github.com/jclement/shellraiser/internal/config"
	"github.com/jclement/shellraiser/internal/server"
)

func main() {
	// cmd-shim mode: stand in for a device-exposed CLI tool (op, gh, …). Invoked
	// as `shellraiser cmd-shim <name> <args…>` by the per-tool shims the entrypoint
	// installs. Handled before flag parsing so tool args pass through untouched.
	if len(os.Args) > 1 && os.Args[1] == "cmd-shim" {
		name := ""
		var rest []string
		if len(os.Args) > 2 {
			name = os.Args[2]
			rest = os.Args[3:]
		}
		os.Exit(runShim(name, rest))
	}

	log.SetFlags(log.Ltime)

	repo := flag.String("repo", envOr("SHELLRAISER_REPO", ""), "project git repo (default: current dir)")
	addr := flag.String("addr", "", "listen address (overrides config)")
	noAuth := flag.Bool("no-auth", false, "disable web UI authentication")
	flag.Parse()

	repoDir := *repo
	if repoDir == "" {
		repoDir, _ = os.Getwd()
	}
	repoDir, _ = filepath.Abs(repoDir)

	// Layered config: defaults → .shellraiser.toml → .shellraiser.local.toml → env.
	cfg, err := config.Load(repoDir)
	if err != nil {
		log.Fatalf("shellraiser: config: %v", err)
	}
	// Explicit flags win over everything.
	if *addr != "" {
		cfg.Addr = *addr
	}
	if *noAuth {
		cfg.NoAuth = true
	}

	srv, err := server.New(repoDir, cfg)
	if err != nil {
		log.Fatalf("shellraiser: %v", err)
	}
	if err := srv.Run(); err != nil {
		log.Fatalf("shellraiser: %v", err)
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
