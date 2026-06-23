package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"text/template"

	"github.com/jclement/shellraiser/cmd/sr/assets"
	"github.com/jclement/shellraiser/internal/config"
	"github.com/jclement/shellraiser/internal/ui"
)

// baseImage is the default shellraiser base (ubuntu + full toolchain), built once
// and reused. Version-pinned so an sr upgrade rebuilds it.
func baseImage() string { return "sr-base:" + version }

// engineArch returns the docker ENGINE architecture (amd64/arm64) — not the host
// arch, since Apple Silicon can run an emulated amd64 engine.
func engineArch() string {
	out, err := dockerOut("version", "-f", "{{.Server.Arch}}")
	if err != nil || out == "" {
		return "amd64"
	}
	return out
}

// workerBinary returns the embedded cross-compiled linux worker for arch.
func workerBinary(arch string) ([]byte, error) {
	b, err := assets.FS.ReadFile("bin/worker-linux-" + arch)
	if err != nil {
		return nil, fmt.Errorf("worker binary for linux/%s not embedded — run `mise run build` to cross-compile it", arch)
	}
	return b, nil
}

// resolveImage returns the worker image tag for a project, building it (and the
// base, if needed) from the embedded assets when missing. The tag is a content
// hash so any change to the base, overlay, worker binary, or arch rebuilds.
func resolveImage(project string) (string, error) {
	cfg, _ := config.Load(project)
	arch := engineArch()
	worker, err := workerBinary(arch)
	if err != nil {
		return "", err
	}

	base, lean, err := resolveBase(project, cfg)
	if err != nil {
		return "", err
	}

	tmplBytes, _ := assets.FS.ReadFile("overlay.Dockerfile.tmpl")
	tmpl, err := template.New("overlay").Parse(string(tmplBytes))
	if err != nil {
		return "", err
	}
	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, map[string]any{"Base": base, "Lean": lean}); err != nil {
		return "", err
	}

	entrypoint, _ := assets.FS.ReadFile("entrypoint.sh")
	h := sha256.New()
	h.Write([]byte(base))
	h.Write([]byte(arch))
	h.Write(rendered.Bytes())
	h.Write(entrypoint) // entrypoint ships in the overlay → must retrigger builds
	h.Write(worker)
	tag := "sr-" + hex.EncodeToString(h.Sum(nil))[:12]

	if imageExists(tag) {
		return tag, nil
	}
	ui.Info("image", "building %s (base %s)…", tag, base)
	if err := buildOverlay(tag, rendered.Bytes(), worker); err != nil {
		return "", err
	}
	ui.Info("image", "built %s", tag)
	return tag, nil
}

// resolveBase returns the base image reference for the overlay and whether the
// overlay must add the lean must-haves (true for a user base, false for the
// shellraiser base which already has them).
func resolveBase(project string, cfg config.Config) (string, bool, error) {
	switch {
	case cfg.Base != "":
		if err := probeAptBase(cfg.Base); err != nil {
			return "", false, err
		}
		return cfg.Base, true, nil
	case cfg.Dockerfile != "":
		base, err := buildUserDockerfile(project, cfg.Dockerfile)
		return base, true, err
	default:
		if err := ensureBaseImage(); err != nil {
			return "", false, err
		}
		return baseImage(), false, nil
	}
}

// ensureBaseImage builds the default shellraiser base from the embedded
// base.Dockerfile if it is not already present.
func ensureBaseImage() error {
	if imageExists(baseImage()) {
		return nil
	}
	ui.Info("image", "building base %s (one-time, a few minutes)…", baseImage())
	dockerfile, _ := assets.FS.ReadFile("base.Dockerfile")
	zshrc, _ := assets.FS.ReadFile("zshrc")
	return dockerBuild(baseImage(), map[string][]byte{
		"Dockerfile": dockerfile,
		"zshrc":      zshrc,
	})
}

// buildOverlay builds the lean overlay image (rendered Dockerfile + worker).
func buildOverlay(tag string, dockerfile, worker []byte) error {
	entrypoint, _ := assets.FS.ReadFile("entrypoint.sh")
	return dockerBuild(tag, map[string][]byte{
		"Dockerfile":    dockerfile,
		"entrypoint.sh": entrypoint,
		"worker":        worker,
	})
}

// buildUserDockerfile builds a project's own Dockerfile as the base, tagged by
// content so edits retrigger it.
func buildUserDockerfile(project, name string) (string, error) {
	path := filepath.Join(project, name)
	df, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", name, err)
	}
	tag := "sr-userbase-" + hashShort(df)
	if imageExists(tag) {
		return tag, nil
	}
	ui.Info("image", "building project base from %s…", name)
	// Build in the project dir so its COPY/ADD context resolves.
	cmd := exec.Command("docker", "build", "-t", tag, "-f", path, project)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("build %s: %w", name, err)
	}
	return tag, nil
}

// probeAptBase fails friendly if a custom base is not Debian/Ubuntu family (the
// overlay needs apt + useradd).
func probeAptBase(base string) error {
	if exec.Command("docker", "run", "--rm", base, "sh", "-c", "command -v apt-get >/dev/null").Run() != nil {
		return fmt.Errorf("base %q has no apt-get — shellraiser bases must be Debian/Ubuntu family", base)
	}
	return nil
}

// dockerBuild writes files into a temp context and runs `docker build`,
// streaming progress to stdout (the daemon log).
func dockerBuild(tag string, files map[string][]byte) error {
	ctx, err := os.MkdirTemp("", "sr-build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(ctx)
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(ctx, name), data, 0o644); err != nil {
			return err
		}
	}
	cmd := exec.Command("docker", "build", "--progress=plain", "-t", tag, ctx)
	cmd.Env = append(os.Environ(), "DOCKER_BUILDKIT=1")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker build %s: %w", tag, err)
	}
	return nil
}

func hashShort(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])[:12]
}
