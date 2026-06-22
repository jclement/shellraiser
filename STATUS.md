# slopbox — build status

What exists right now, and what's still rough. See [idea.md](idea.md) for the
vision and [DESIGN.md](DESIGN.md) for the architecture.

## Works (validated)

- **Go web app** — single binary, embeds the UI (`cmd/slopbox`, `internal/*`,
  `web/`). Runs as non-root `ubuntu` (uid 1000), `$HOME` + tool PATHs integrated.
- **Worktrees** — list / create / remove via real `git worktree`, with live
  **git stats**: `+added/−deleted`, commits ahead of base, **dirty** flag,
  **⇡/⇣ vs origin**.
- **Sessions** — claude / codex / shell / editor on real PTYs; ring-buffer
  replay on (re)attach; live xterm.js over a websocket bridge.
- **Activity + ding** — running/idle/exited state machine; agents chime when
  they finish a unit of work (SSE → WebAudio).
- **Ports → worktrees** — listening ports detected (`ss`/`lsof`) and attributed
  to the worktree whose session opened them, via `/proc` PID ancestry.
- **Custom commands** — extra launcher buttons from `.slopbox.toml`.
- **Config** — `.slopbox.toml` → `.slopbox.local.toml` → env, with precedence;
  commands are toml-only.
- **Auth: passkeys (WebAuthn)** — bootstrap code (logged) → register a passkey →
  sign in with it; add more anytime. Per-origin RP IDs discovered from Host, or
  pinned via `rp_id`. `SLOPBOX_TOKEN` fallback for automation; `--no-auth` for
  trusted local/testing.
- **Locked-down surface** — default-deny routing (only the login shell +
  `/api/auth/*` are public), websocket origin checks (CSWSH), bootstrap-code rate
  limiting. pgweb/code-server/postgres bind `127.0.0.1` only; the data APIs and
  both proxies all 401 until you've passkey'd in (verified).
- **Edit in VS Code** — `code-server` proxied at `/edit` (subpath, websockets,
  GitLens); ✎ on a worktree opens it scoped to that folder. Behind auth.
- **Postgres** (`postgres/postgres`) + **pgweb at `/db`** (reverse-proxied with
  `--prefix=db`), data on its own volume; degrades gracefully if it can't init.
- **`slopbox.sh` manager** — `start/list/stop/ish/open/logs/nuke`, fzf box-picker
  with preview; `run.sh` is an alias for `start`.
- **Tunnels** — `cloudflared` and the `gatecrash` client are in the image,
  started from env vars.
- **Image** — multi-stage Dockerfile: Ubuntu + zsh/starship, vim/helix/Fresh,
  mise, Node, claude/codex, docker client, postgres/pgweb, cloudflared/gatecrash.
- **`run.sh`**, **`docker-compose.yml`**, **CI** (multi-arch → GHCR).

### Verified end-to-end
- `go build`/`vet`/`gofmt` clean; image builds (3 GB).
- Running container: postgres up, `/db` 200, ports attributed, tools present,
  app runs as `ubuntu` with `HOME=/home/ubuntu`.
- **Playwright (8/8)** in a real browser: worktree list + git stats render, a
  shell session streams live terminal output (starship prompt), no console
  errors, and the full **passkey register → logout → login** round-trip works
  (virtual authenticator). `--no-auth` is the test entry point.

## Stubbed / TODO

- **Homebrew bootstrap** — the brew prefix is symlinked into the home mount and
  `Brewfile`/`brew bundle` runs on startup, but brew itself isn't installed on
  first run yet.
- **Session persistence across slopbox restarts** — sessions live in-process
  (survive disconnects, not a process restart). tmux-backed is the planned fix.
- **Overflow resync** — on a slow-consumer drop, replay the ring to resync xterm.
- **CDN assets** — Tailwind/xterm/simplewebauthn load from CDNs; vendor + embed
  for a fully offline, CSP-tight image.
- **GPG/SSH agent forwarding** — not wired (see idea.md).
- **Passkey management UI** — add/remove works via API + a basic button; a real
  credentials list/management panel would be nicer.
- **Image size** — ~3 GB; slim the toolchain layers.
</content>
