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

// cmdLs renders the status dashboard: coordinator header + a color-keyed row per
// project. `sb` and `sb status` share it.
func cmdLs(_ []string) {
	if !dockerAlive() {
		fatal("docker is not running")
	}
	dir, _ := globalDir()
	ui.Print("")

	// Coordinator header.
	coordLine := ui.Gray("coordinator  ") + ui.Red("● down")
	if m, ok := liveCoordinator(dir); ok {
		coordLine = ui.Gray("coordinator  ") + ui.Green("● up") +
			ui.Gray("   ui ") + ui.Cyan("http://127.0.0.1:"+m.Port+"/")
	}
	ui.Print("  " + ui.Accent("▟█▙ slopbox") + "  " + coordLine)
	img := ui.Green("✔")
	if !imageExists(baseImage()) {
		img = ui.Gray("not built yet")
	}
	ui.Print("  " + ui.Accent("▜█▛") + ui.Gray("  base ") + baseImage() + "  " + img)
	ui.Print("")

	workers := reconciledRegistry().list()
	if len(workers) == 0 {
		ui.Print("  " + ui.Gray("no projects registered — run `sb` in a git repo"))
		return
	}
	for _, w := range workers {
		dot := ui.Green("●")
		if w.State != "running" {
			dot = ui.Gray("○")
		}
		ports := ""
		if w.APIPort != "" {
			ports = ui.Gray("  api :" + w.APIPort)
			if w.SSHPort != "" {
				ports += ui.Gray("  ssh :" + w.SSHPort)
			}
		}
		ui.Print(fmt.Sprintf("  %s %s %s%s", dot, ui.Bold(pad(w.ID, 18)), ui.Gray(w.State), ports))
		ui.Print("    " + ui.Dim(w.Project))
	}
}

func pad(s string, n int) string {
	for len(s) < n {
		s += " "
	}
	return s
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
	check("worker arch", true, "linux/"+engineArch())
	if _, err := workerBinary(engineArch()); err != nil {
		check("worker binary", false, err.Error())
	} else {
		check("worker binary", true, "embedded")
	}
	if imageExists(baseImage()) {
		check("base image", true, baseImage())
	} else {
		check("base image", true, baseImage()+" — pending (builds on first run)")
	}
	if dockerAlive() {
		// No managed worker should sit on the default bridge (isolation invariant).
		out, _ := dockerOut("ps", "--filter", "label=slopbox.role=worker",
			"--filter", "network=bridge", "--format", "{{.Names}}")
		check("network isolation", out == "", "no workers on the default bridge")
		check("workers", true, fmt.Sprintf("%d registered", len(reconciledRegistry().list())))
	}
}
