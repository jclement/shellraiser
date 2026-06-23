# shellraiser — build status

What exists right now. See [ARCHITECTURE.md](ARCHITECTURE.md) for the v2 design
and [README.md](README.md) for usage. (v1's [DESIGN.md](DESIGN.md) describes the
single-box model the worker is derived from.)

## v2 — the `sr` coordinator (validated)

- **Coordinator** (`cmd/sr`) — one host binary fronts many per-project worker
  containers behind one UI + one port. Detached, self-daemonizing: `cd p1 && sr`
  then `cd p2 && sr` join the same coordinator (flock single-instance, unix-socket
  control plane). Registry reconciled from docker labels (re-adopts on restart).
- **Password auth** — one coordinator password (bcrypt), enforced before every
  proxy hop (HTTP+WS). First run mints a one-time password (printed to the log);
  sign in, then set a real one. Change it from the UI Settings dialog. No passkeys,
  so it's identical on localhost and the tailnet. *(curl + Playwright verified.)*
- **Unified UI** — project rail → worktrees → sessions; `/w/<id>/` proxy; container
  stop/start/nuke controls; Settings dialog. *(Playwright: `test/e2e/coordinator.sh`.)*
- **Global config** (`~/.config/shellraiser/config.toml`, 0600): password hash +
  `ssh_passthrough` / `git_passthrough`, editable from the UI Settings dialog.
- **SSH/git passthrough** — forward the host SSH agent (YubiKey; engine-aware) +
  bind `~/.ssh` config/known_hosts and `~/.gitconfig` into workers so git/ssh just
  work. Off by default. *(Verified: mounts/env/seeding present.)*
- **Hardened workers** — per-worker docker network, `--memory`/`--pids` caps,
  loopback-only API fenced by a per-worker token, hardened sshd.
- **SSH port-mapper** — declared `ports` auto-forwarded to host loopback via SSH
  `-L`; discovered ports get a UI map toggle; reserved-port denylist; loopback
  binds (+ the tailnet IP when `--tailnet` is on). *(Loopback verified end-to-end.)*
- **Self-contained image build** — `sr` embeds the Dockerfile assets + the
  cross-compiled linux worker binaries and builds `sr-base` + a content-hash
  overlay locally (no registry). Custom `base`/`dockerfile`. *(Verified incl.
  `base = "node:20"`.)*
- **Density** — postgres off by default; idle auto-stop after 30m with lazy-resume
  on next request. *(Verified.)*
- **Shared agent logins** — creds-only `:ro` mount, per-worker
  `CLAUDE_CONFIG_DIR`/`CODEX_HOME`, `sr login` single-writer, `isolated_agents`
  opt-out. *(Verified.)*
- **Tailscale via `tsnet`** — `sr --tailnet` exposes the UI (and mapped ports) on a
  single host-side tailnet node (state in `~/.config/shellraiser/tsnet`), serving
  the same gated handler over HTTPS so the password still applies; `--no-auth +
  --tailnet` is refused. *(Node init + refusal verified; live exposure needs your
  authenticated tailnet.)*
- **CLI** — `sr`, `ls`/`status` (color dashboard), `stop`, `nuke`, `logs`,
  `login`, `down`, `doctor`. goreleaser → Homebrew (`sr`).

## Inherited from v1 (the worker backend)

- Worktrees with live git stats; PTY sessions (claude/codex/shell/editor) with
  ring-buffer replay + WS reconnect; activity/ding; port detection; `/p/` proxy;
  code-server `/edit`; opt-in postgres + `/db`; mobile keyboard chrome.
