package main

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/jclement/slopbox/internal/ui"
)

// isGitRepo reports whether dir is inside a git work tree.
func isGitRepo(dir string) bool {
	return exec.Command("git", "-C", dir, "rev-parse", "--is-inside-work-tree").Run() == nil
}

// waitReady polls the worker's API until it answers (or gives up).
func waitReady(w *Worker) {
	for i := 0; i < 80; i++ {
		req, _ := http.NewRequest("GET", "http://127.0.0.1:"+w.APIPort+"/api/info", nil)
		if w.Token != "" {
			req.Header.Set("X-Slopbox-Worker", w.Token)
		}
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	ui.Warn("sb", "worker %s did not answer in time — continuing", w.Container)
}

// reconciledRegistry returns a registry populated from docker, for the
// query/lifecycle subcommands that run without a coordinator process.
func reconciledRegistry() *Registry {
	r := newRegistry()
	r.reconcile()
	return r
}

func cmdLs(_ []string) {
	if !dockerAlive() {
		fatal("docker is not running")
	}
	workers := reconciledRegistry().list()
	if len(workers) == 0 {
		fmt.Println("no projects registered")
		return
	}
	for _, w := range workers {
		fmt.Printf("  %-20s %-9s %s\n", w.ID, w.State, w.Project)
	}
}

func cmdStop(args []string) {
	if !dockerAlive() {
		fatal("docker is not running")
	}
	reg := reconciledRegistry()
	targets := reg.list()
	if len(args) > 0 {
		w, ok := reg.get(args[0])
		if !ok {
			fatal("no such project: %s", args[0])
		}
		targets = []*Worker{w}
	}
	for _, w := range targets {
		if w.State != "running" {
			continue
		}
		if _, err := dockerRun("stop", w.Container); err != nil {
			ui.Warn("sb", "stop %s: %v", w.ID, err)
			continue
		}
		ui.Info("sb", "stopped %s", w.ID)
	}
}

func cmdNuke(args []string) {
	if len(args) == 0 {
		fatal("usage: sb nuke <id>")
	}
	if !dockerAlive() {
		fatal("docker is not running")
	}
	id := args[0]
	reg := reconciledRegistry()
	w, ok := reg.get(id)
	if !ok {
		fatal("no such project: %s", id)
	}
	_, _ = dockerRun("rm", "-f", w.Container)
	_, _ = dockerRun("volume", "rm", w.Volume)
	_ = exec.Command("docker", "network", "rm", w.Network).Run()
	ui.Info("sb", "nuked %s (container + volume + network) — project source untouched", id)
}

func cmdLogs(args []string) {
	if len(args) == 0 {
		fatal("usage: sb logs <id>")
	}
	c := containerName(args[0])
	cmd := exec.Command("docker", "logs", "-f", "--tail", "100", c)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		os.Exit(1)
	}
}

func cmdDoctor(_ []string) {
	check := func(name string, ok bool, detail string) {
		mark := "ok"
		if !ok {
			mark = "FAIL"
		}
		fmt.Printf("  [%-4s] %-22s %s\n", mark, name, detail)
	}
	dir, err := globalDir()
	check("global dir", err == nil, dir)
	check("docker", dockerAlive(), "daemon reachable")
	check("worker image", imageExists(workerImage), workerImage)
	if dockerAlive() {
		// No managed worker should sit on the default bridge (isolation invariant).
		out, _ := dockerOut("ps", "--filter", "label=slopbox.role=worker",
			"--filter", "network=bridge", "--format", "{{.Names}}")
		check("network isolation", out == "", "no workers on the default bridge")
		check("workers", true, fmt.Sprintf("%d registered", len(reconciledRegistry().list())))
	}
}
