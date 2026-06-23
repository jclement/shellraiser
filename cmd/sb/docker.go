package main

import (
	"fmt"
	"os/exec"
	"strings"
)

// dockerOut runs a docker command and returns trimmed stdout.
func dockerOut(args ...string) (string, error) {
	out, err := exec.Command("docker", args...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// dockerRun runs a docker command, returning combined output on failure so the
// caller can surface the real error (not just "exit status 1").
func dockerRun(args ...string) (string, error) {
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// dockerAlive reports whether the docker daemon is reachable.
func dockerAlive() bool {
	return exec.Command("docker", "info").Run() == nil
}

// imageExists reports whether a local image is present.
func imageExists(img string) bool {
	return exec.Command("docker", "image", "inspect", img).Run() == nil
}

// containerState returns docker's State.Status (running, exited, …) or "" if the
// container does not exist.
func containerState(name string) string {
	s, err := dockerOut("inspect", "-f", "{{.State.Status}}", name)
	if err != nil {
		return ""
	}
	return s
}

// publishedPort returns the loopback host port mapped to containerPort/tcp.
func publishedPort(name, containerPort string) (string, error) {
	tmpl := fmt.Sprintf(`{{(index (index .NetworkSettings.Ports "%s/tcp") 0).HostPort}}`, containerPort)
	p, err := dockerOut("inspect", "-f", tmpl, name)
	if err != nil {
		return "", fmt.Errorf("inspect %s port %s: %w", name, containerPort, err)
	}
	if p == "" {
		return "", fmt.Errorf("%s has no published %s port yet", name, containerPort)
	}
	return p, nil
}

// containerEnv reads one environment variable's value from a container's config.
func containerEnv(name, key string) string {
	out, err := dockerOut("inspect", "-f", "{{range .Config.Env}}{{println .}}{{end}}", name)
	if err != nil {
		return ""
	}
	for _, ln := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(ln, key+"="); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// removeNetwork best-effort deletes a user-defined network (ignores "in use" /
// "not found").
func removeNetwork(name string) error {
	return exec.Command("docker", "network", "rm", name).Run()
}

// ensureNetwork creates a user-defined bridge network if it does not exist, so a
// danger-mode worker is L3-isolated from its siblings (the default bridge lets
// any container reach every other container's published-internally services).
func ensureNetwork(name string) error {
	if exec.Command("docker", "network", "inspect", name).Run() == nil {
		return nil
	}
	_, err := dockerRun("network", "create", name)
	return err
}
