// Command sb is the slopbox host coordinator (v2). Phase 1: ensure a worker
// container for the current project and serve its UI through one coordinator
// port. Multi-project aggregation, SSH port-mapping, and tsnet come next.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const workerImage = "slopbox:local" // built from the repo Dockerfile (embedded in a later phase)

func main() {
	log.SetFlags(log.Ltime)
	noAuth := flag.Bool("no-auth", false, "disable web auth")
	port := flag.String("port", "7700", "coordinator UI port")
	flag.Parse()

	project := flag.Arg(0)
	if project == "" {
		project, _ = os.Getwd()
	}
	project, _ = filepath.Abs(project)
	if !isGitRepo(project) {
		log.Fatalf("sb: %s is not a git repository", project)
	}
	if !imageExists(workerImage) {
		log.Fatalf("sb: image %s not found — build it first (./slopbox.sh start --rebuild, image build coming to sb)", workerImage)
	}

	id := boxID(project)
	workerPort, err := ensureWorker(id, project, *noAuth)
	if err != nil {
		log.Fatalf("sb: %v", err)
	}
	waitReady(workerPort)

	// Phase 1: proxy everything to the single worker. Phase 2 mounts workers
	// under /w/<id>/ and serves a unified shell.
	target, _ := url.Parse("http://127.0.0.1:" + workerPort)
	proxy := httputil.NewSingleHostReverseProxy(target)

	log.Printf("sb: coordinator on http://localhost:%s/", *port)
	log.Printf("sb: project %q → worker %s (127.0.0.1:%s)", id, container(id), workerPort)
	if err := http.ListenAndServe(":"+*port, proxy); err != nil {
		log.Fatal(err)
	}
}

func container(id string) string { return "sb_" + id }
func volume(id string) string    { return "sb_" + id + "_vol" }

// ensureWorker starts (or reuses) the worker container and returns its
// loopback-published host port for :7000.
func ensureWorker(id, project string, noAuth bool) (string, error) {
	c := container(id)
	if running(c) {
		log.Printf("sb: reusing worker %s", c)
		return hostPort(c)
	}
	_ = exec.Command("docker", "rm", "-f", c).Run() // clear a stopped one
	args := []string{
		"run", "-d", "--name", c,
		"--label", "slopbox.id=" + id, "--label", "slopbox.project=" + project,
		"-v", project + ":/work",
		"-v", volume(id) + ":/home/ubuntu",
		"-p", "127.0.0.1:0:7000", // loopback only; the coordinator fronts it
		"-e", "SLOPBOX_REPO=/work", "-e", "SLOP_ID=" + id,
	}
	if noAuth {
		args = append(args, "-e", "SLOPBOX_NO_AUTH=1")
	}
	args = append(args, workerImage)
	if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		return "", fmt.Errorf("start worker: %s", strings.TrimSpace(string(out)))
	}
	log.Printf("sb: started worker %s", c)
	return hostPort(c)
}

func hostPort(c string) (string, error) {
	out, err := exec.Command("docker", "inspect", "-f",
		`{{(index (index .NetworkSettings.Ports "7000/tcp") 0).HostPort}}`, c).Output()
	if err != nil {
		return "", fmt.Errorf("inspect port: %w", err)
	}
	p := strings.TrimSpace(string(out))
	if p == "" {
		return "", fmt.Errorf("worker %s has no published port yet", c)
	}
	return p, nil
}

func waitReady(port string) {
	for i := 0; i < 60; i++ {
		if resp, err := http.Get("http://127.0.0.1:" + port + "/api/info"); err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func running(c string) bool {
	out, _ := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", c).Output()
	return strings.TrimSpace(string(out)) == "true"
}

func imageExists(img string) bool {
	return exec.Command("docker", "image", "inspect", img).Run() == nil
}

func isGitRepo(dir string) bool {
	return exec.Command("git", "-C", dir, "rev-parse", "--is-inside-work-tree").Run() == nil
}

// boxID is the project's id: `id` in .slopbox.toml, else the folder name.
func boxID(project string) string {
	for _, f := range []string{".slopbox.toml", ".slopbox.local.toml"} {
		b, err := os.ReadFile(filepath.Join(project, f))
		if err != nil {
			continue
		}
		for _, ln := range strings.Split(string(b), "\n") {
			ln = strings.TrimSpace(ln)
			if strings.HasPrefix(ln, "id") {
				if i := strings.Index(ln, "="); i >= 0 {
					if v := strings.Trim(strings.TrimSpace(ln[i+1:]), `"' `); v != "" {
						return v
					}
				}
			}
		}
	}
	return filepath.Base(project)
}
