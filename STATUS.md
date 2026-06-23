# slopbox — build status

What exists right now. See [ARCHITECTURE.md](ARCHITECTURE.md) for the v2 design
and [README.md](README.md) for usage. (v1's [DESIGN.md](DESIGN.md) describes the
single-box model the worker is derived from.)

## v2 — the `sb` coordinator (validated)

- **Coordinator** (`cmd/sb`) — one host binary fronts many per-project worker
  containers behind one UI + one port. Detached, self-daemonizing: `cd p1 && sb`
  then `cd p2 && sb` join the same coordinator (flock single-instance, unix-socket
  control plane). Registry reconciled from docker labels (re-adopts on restart).
- **Unified UI** — one passkey login (enforced before every proxy hop, HTTP+WS);
  project rail → worktrees → sessions; `/w/<id>/` proxy; container stop/start/nuke
  controls. *(Playwright-verified: `test/e2e/coordinator.sh`.)*
- **Hardened workers** — per-worker docker network, `--memory`/`--pids` caps,
  loopback-only API fenced by a per-worker token, hardened sshd.
- **SSH port-mapper** — declared `ports` auto-forwarded to host loopback via SSH
  `-L`; discovered ports get a UI map toggle; reserved-port denylist; loopback-only
  binds. *(Verified end-to-end.)*
- **Self-contained image build** — `sb` embeds the Dockerfile assets + the
  cross-compiled linux worker binaries and builds `sb-base` + a content-hash
  overlay locally (no registry). Custom `base`/`dockerfile`. *(Verified incl.
  `base = "node:20"`.)*
- **Density** — postgres off by default; idle auto-stop after 30m with lazy-resume
  on next request. *(Verified.)*
- **Shared agent logins** — creds-only `:ro` mount, per-worker
  `CLAUDE_CONFIG_DIR`/`CODEX_HOME`, `sb login` single-writer, `isolated_agents`
  opt-out. *(Verified.)*
- **CLI** — `sb`, `ls`/`status` (color dashboard), `stop`, `nuke`, `logs`,
  `login`, `down`, `doctor`. goreleaser → Homebrew (`sb`).

## Inherited from v1 (the worker backend)

- Worktrees with live git stats; PTY sessions (claude/codex/shell/editor) with
  ring-buffer replay + WS reconnect; activity/ding; port detection; `/p/` proxy;
  code-server `/edit`; opt-in postgres + `/db`; mobile keyboard chrome.

## Not yet done

- **Tailscale via `tsnet`** (host-side single node + UI/port exposure on the
  tailnet). The in-container Tailscale path from v1 still works in the meantime.
- RP-ID pinning to an enumerated origin set (lands with tsnet).
