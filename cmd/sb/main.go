// Command sb is the slopbox host coordinator (v2). One sb fronts many per-project
// worker containers behind a single UI and port. Bare `sb` ensures the
// coordinator is up, registers the current directory as a worker, and opens the
// UI; subcommands manage the fleet.
//
// This is the Phase-2 coordinator core: a foreground coordinator that reconciles
// every managed worker from docker labels and proxies each under /w/<id>/. The
// detached daemon + unix-socket control plane land in a later increment.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jclement/slopbox/internal/ui"
)

const workerImage = "slopbox:local" // built from the embedded Dockerfile in Phase 5

func main() {
	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 && !isFlag(args[0]) {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "", "up":
		cmdUp(args)
	case "ls", "status":
		cmdLs(args)
	case "stop":
		cmdStop(args)
	case "nuke":
		cmdNuke(args)
	case "logs":
		cmdLogs(args)
	case "doctor":
		cmdDoctor(args)
	case "help", "-h", "--help":
		usage()
	default:
		fatal("unknown command %q (try `sb help`)", cmd)
	}
}

func isFlag(s string) bool { return len(s) > 0 && s[0] == '-' }

func usage() {
	fmt.Print(`sb — slopbox coordinator

  sb [DIR]        ensure the coordinator, register DIR (default: cwd), open the UI
  sb ls           list registered projects
  sb stop  [id]   stop a worker (all if omitted)
  sb nuke   id    remove a worker container + its volume
  sb logs   id    stream a worker's container logs
  sb doctor       preflight checks (docker, image, perms)
  sb help         this message

flags (for bare sb): --no-auth, --port <p>
`)
}

// cmdUp is the default: ensure the coordinator is running and the cwd is
// registered as a worker, then serve. (Pre-daemon: this process IS the
// coordinator and adopts every other managed worker via reconcile.)
func cmdUp(args []string) {
	var noAuth bool
	port := "7700"
	project := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--no-auth":
			noAuth = true
		case "--port":
			if i+1 < len(args) {
				i++
				port = args[i]
			}
		default:
			if !isFlag(args[i]) && project == "" {
				project = args[i]
			}
		}
	}
	if project == "" {
		project, _ = os.Getwd()
	}
	project, _ = filepath.Abs(project)

	if _, err := globalDir(); err != nil {
		fatal("%v", err)
	}
	if !dockerAlive() {
		fatal("docker is not running — start Docker Desktop and retry")
	}
	if !isGitRepo(project) {
		fatal("%s is not a git repository (run `git init` first)", project)
	}
	if !imageExists(workerImage) {
		fatal("image %s not found — build it first (`mise run dev` / image build lands in Phase 5)", workerImage)
	}

	id := boxID(project)
	ui.Boot("sb", "project", id, "path", project)
	w, err := ensureWorker(id, project, noAuth)
	if err != nil {
		fatal("%v", err)
	}
	waitReady(w)

	co := newCoordinator(port)
	co.reg.put(w)
	co.reg.reconcile() // adopt any other already-running workers
	ui.Info("sb", "project %q → worker %s (api 127.0.0.1:%s)", id, w.Container, w.APIPort)
	if err := co.Run(); err != nil {
		fatal("%v", err)
	}
}

func fatal(format string, a ...any) {
	ui.Warn("sb", format, a...)
	os.Exit(1)
}
