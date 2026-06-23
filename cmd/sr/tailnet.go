package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"

	"github.com/jclement/shellraiser/internal/ui"
	"tailscale.com/tsnet"
)

// newTailnetServer builds (does not start) the single tsnet node — no host
// tailscaled, no per-worker nodes. State lives in <globalDir>/tsnet. With no
// TAILSCALE_KEY, tsnet prints a one-time login URL to the coordinator log.
func newTailnetServer(dir string) *tsnet.Server {
	return &tsnet.Server{
		Dir:      filepath.Join(dir, "tsnet"),
		Hostname: "shellraiser",
		AuthKey:  os.Getenv("TAILSCALE_KEY"), // optional
	}
}

// serveTailnetUI exposes the coordinator UI on the tailnet, serving the SAME
// gated handler — the password is still required; the tailnet is a second
// factor, never a replacement. HTTPS (the tailnet cert) gives a secure context.
func serveTailnetUI(c *Coordinator, s *tsnet.Server) {
	if os.Getenv("TAILSCALE_KEY") == "" {
		ui.Info("tailnet", "first run — find the login URL in the coordinator log (sr logs)")
	}
	ln, err := s.ListenTLS("tcp", ":443")
	if err != nil {
		ui.Warn("tailnet", "could not listen (is HTTPS enabled for your tailnet?): %v", err)
		return
	}
	if st, err := s.Up(context.Background()); err == nil {
		if len(st.CertDomains) > 0 {
			ui.Info("tailnet", "UI on https://%s/ (password still required)", st.CertDomains[0])
		} else if len(st.TailscaleIPs) > 0 {
			ui.Info("tailnet", "UI on the tailnet at %s (password still required)", st.TailscaleIPs[0])
		}
	}
	_ = http.Serve(ln, c.httpHandler())
}
