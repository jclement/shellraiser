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
