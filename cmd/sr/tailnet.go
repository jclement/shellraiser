package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"

	"github.com/jclement/shellraiser/internal/ui"
	"tailscale.com/tsnet"
)

// serveTailnet exposes the coordinator UI on the tailnet via a single tsnet node
// (no host tailscaled, no per-worker nodes). It serves the SAME gated handler, so
// passkey auth is still required — the tailnet is a second factor, never a
// replacement. HTTPS (via the tailnet's cert) gives WebAuthn a secure context.
//
// State lives in <globalDir>/tsnet. With no TAILSCALE_KEY, tsnet prints a one-time
// login URL to the coordinator log; authenticate once and it persists.
func serveTailnet(c *Coordinator, dir string) {
	s := &tsnet.Server{
		Dir:      filepath.Join(dir, "tsnet"),
		Hostname: "shellraiser",
		AuthKey:  os.Getenv("TAILSCALE_KEY"), // optional
		// Surface tsnet's own logs (incl. the first-run "authenticate at …" URL)
		// to the coordinator log so the user can complete login via `sr logs`.
	}
	defer s.Close()
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
			ui.Info("tailnet", "UI on https://%s/ (passkey still required)", st.CertDomains[0])
		} else if len(st.TailscaleIPs) > 0 {
			ui.Info("tailnet", "UI on the tailnet at %s (passkey still required)", st.TailscaleIPs[0])
		}
	}
	_ = http.Serve(ln, c.httpHandler())
}
