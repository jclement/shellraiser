# slopbox e2e tests

Playwright browser tests covering the real UI: worktree rendering, a live
terminal session, the full passkey register → logout → login round-trip (via a
CDP virtual authenticator), and light/dark theming.

```bash
mise run e2e        # builds slopbox, starts two instances, runs the suite
```

`run.sh` is self-contained: it builds the binary, installs Playwright + chromium
on first run, starts a `--no-auth` instance (UI/terminal/theme checks) and an
auth-enabled instance (passkey checks), runs `e2e.mjs`, and tears everything
down. No Docker required.
