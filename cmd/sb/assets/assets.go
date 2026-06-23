// Package assets holds everything `sb` needs to build a worker image locally:
// the base Dockerfile, the lean overlay template, the entrypoint, and the
// cross-compiled linux worker binaries. They are embedded so the single `sb`
// binary can build images on any machine with no registry and no Go toolchain.
package assets

import "embed"

// FS holds the embedded build assets. The bin/ directory carries the
// cross-compiled worker binaries (worker-linux-amd64, worker-linux-arm64),
// produced by `mise run build` and dropped in before `go build ./cmd/sb`. The
// `all:` prefix keeps the committed .gitkeep so the package always compiles.
//
//go:embed base.Dockerfile overlay.Dockerfile.tmpl entrypoint.sh zshrc all:bin
var FS embed.FS
