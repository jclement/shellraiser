// Command sr is the shellraiser host coordinator (v2). One sr fronts many per-project
// worker containers behind a single UI and port. Bare `sr` ensures the
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

	"github.com/jclement/shellraiser/internal/ui"
)

func main() {
	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 && !isFlag(args[0]) {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "", "up":
		cmdUp(args)
	case "__daemon":
		cmdDaemon(args)
	case "down":
		cmdDown(args)
	case "ls", "status":
		cmdLs(args)
	case "stop":
		cmdStop(args)
	case "nuke":
		cmdNuke(args)
	case "logs":
		cmdLogs(args)
	case "login":
		cmdLogin(args)
	case "doctor":
		cmdDoctor(args)
	case "help", "-h", "--help":
		usage()
	default:
		fatal("unknown command %q (try `sr help`)", cmd)
	}
}

func isFlag(s string) bool { return len(s) > 0 && s[0] == '-' }

func usage() {
	fmt.Print(`sr — shellraiser coordinator

  sr [DIR]        ensure the coordinator, register DIR (default: cwd), open the UI
  sr ls           list registered projects
  sr stop  [id]   stop a worker (all if omitted)
  sr nuke   id    remove a worker container + its volume
  sr logs   id    stream a worker's container logs
  sr login        log into claude/codex once (shared across projects)
  sr doctor       preflight checks (docker, image, perms)
  sr help         this message

flags (for bare sr): --no-auth, --port <p>, --tailnet (expose UI on the tailnet),
                     --fg (run the coordinator in the foreground; live logs, Ctrl-C stops)
`)
}

// cmdUp is the default: ensure the coordinator is running and the cwd is
// registered as a worker, then serve. (Pre-daemon: this process IS the
// coordinator and adopts every other managed worker via reconcile.)
func cmdUp(args []string) {
	var noAuth, tailnet, fg bool
	port := "" // empty → a stable random high port (persisted in the global config)
	project := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--no-auth":
			noAuth = true
		case "--tailnet":
			tailnet = true
		case "--fg":
			fg = true
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
	if tailnet && noAuth {
		fatal("--no-auth cannot be combined with --tailnet (an unauthenticated UI must never reach the tailnet)")
	}
	if project == "" {
		project, _ = os.Getwd()
	}
	project, _ = filepath.Abs(project)

	dir, err := globalDir()
	if err != nil {
		fatal("%v", err)
	}
	if port == "" {
		port = resolveUIPort(dir) // stable random high port, persisted
	}
	if !isBareMetal(project) && !dockerAlive() {
		fatal("docker is not running — start Docker Desktop and retry")
	}
	if !isGitRepo(project) {
		fatal("%s is not a git repository (run `git init` first)", project)
	}

	// Build the worker image from embedded assets up-front so progress streams to
	// THIS terminal (the first-run base build takes a few minutes); register then
	// only has to start the container. Bare-metal projects need no image.
	ui.Boot("sr", "project", boxID(project), "path", project)
	var image string
	if !isBareMetal(project) {
		if image, err = resolveImage(project); err != nil {
			fatal("%v", err)
		}
	}

	// Foreground dev mode: run the coordinator in THIS process (live logs,
	// Ctrl-C tears the worker down), with this project registered. Refuses if a
	// detached coordinator already holds the lock.
	if fg {
		runDaemon(dir, port, noAuth, tailnet, project, image)
		return
	}

	m, err := ensureCoordinator(dir, port, noAuth, tailnet)
	if err != nil {
		fatal("%v", err)
	}
	id, err := registerWithDetails(m, project, image)
	if err != nil {
		fatal("%v", err)
	}
	url := fmt.Sprintf("http://127.0.0.1:%s/w/%s/", m.Port, id)
	ui.Ready(url)
	openBrowser(url)
}

// cmdDaemon is the hidden detached-coordinator entrypoint (sr __daemon).
func cmdDaemon(args []string) {
	port := "7700"
	noAuth, tailnet := false, false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--no-auth":
			noAuth = true
		case "--tailnet":
			tailnet = true
		case "--port":
			if i+1 < len(args) {
				i++
				port = args[i]
			}
		}
	}
	dir, err := globalDir()
	if err != nil {
		fatal("%v", err)
	}
	runDaemon(dir, port, noAuth, tailnet, "", "")
}

// cmdDown stops every worker and shuts the coordinator down (end of day).
func cmdDown(_ []string) {
	dir, err := globalDir()
	if err != nil {
		fatal("%v", err)
	}
	for _, w := range reconciledRegistry().list() {
		if w.State == "running" {
			runTeardown(w)
			_, _ = dockerRun("stop", w.Container)
		}
	}
	if m, ok := liveCoordinator(dir); ok {
		_, _ = sockClient(m.Sock).Post("http://unix/shutdown", "application/json", nil)
	}
	ui.Info("sr", "all workers stopped; coordinator shut down")
}

func fatal(format string, a ...any) {
	ui.Warn("sr", format, a...)
	os.Exit(1)
}
