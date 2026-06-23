# sbox — architecture (v2: coordinator + workers)

A rethink of slopbox: instead of one box = one container = one UI/port, a single
host **coordinator** (`sb`) fronts many per-project **worker** containers behind
**one UI and one port**.

```
            ┌─────────────────────── your machine ───────────────────────┐
            │                                                             │
   browser ─┼─▶ sb (coordinator, host binary)                            │
 tailnet ───┼─▶   • one UI (unified: all projects/worktrees/sessions)     │
            │     • one passkey auth                                      │
            │     • builds sb-<ver> image from embedded Dockerfile        │
            │     • per-project worker lifecycle                          │
            │     • dynamic port mapping (host/tailnet ⇄ worker, via SSH) │
            │     • optional Tailscale via tsnet (no host tailscale)      │
            │        │            │                                       │
            │   proxy│api+ws  ssh │tunnels                                │
            │        ▼            ▼                                       │
            │   ┌─ sb_project1 ─┐  ┌─ sb_project2 ─┐   (worker containers)│
            │   │ worker API    │  │ worker API    │   = today's app,     │
            │   │ PTY sessions  │  │ PTY sessions  │     headless backend │
            │   │ /p/ proxy     │  │ /p/ proxy     │                      │
            │   │ code-server   │  │ postgres      │                      │
            │   └───────────────┘  └───────────────┘                      │
            └─────────────────────────────────────────────────────────────┘
```

## Pieces

**`sb` — host binary** (mac/linux/windows × amd64/arm64, Homebrew, versioned).
Single static binary that **embeds**: the Dockerfile, the linux worker binary
(amd64+arm64), and the web UI assets. Responsibilities:
- CLI: `sb` (start/attach coordinator + register cwd as a worker), `sb --no-auth`,
  plus `sb list / stop / nuke / logs / ish`.
- Ensure the `sb-<ver>` image exists — build it locally from the embedded
  Dockerfile + worker binary if missing (no registry needed).
- Run the coordinator: serve the unified UI on **one** port; reverse-proxy each
  worker's API/ws under `/w/<id>/…`; aggregate them into one view.
- **Port mapping:** hold an SSH connection per worker (sshd already exists) and
  add/remove `-L` forwards live as ports appear — bound to `localhost` and/or the
  tailnet IP. This is the only mechanism that reaches arbitrary container TCP on
  **both Mac and Linux**. `/p/<port>/` stays for HTTP/iPad.
- **Tailscale (optional)** via `tsnet` (Tailscale-as-a-library): `sb` is its own
  tailnet node and exposes the UI (and mapped ports) without touching the host's
  Tailscale. State in the global dir.

**Worker — container** = today's app, demoted to a backend (API + PTY + `/p/` +
code-server + postgres). UI now lives in the coordinator. One container
`sb_<id>` + one named volume `sb_<id>_vol` per project. Publishes nothing public;
the coordinator reaches it via a loopback-published API port + SSH.

## Decisions (locked)

- **Unified UI.** One sidebar: projects → worktrees → sessions; one cross-project
  view. The current `web/` app gains a project dimension; all calls go to
  `/w/<id>/api/…` and `/w/<id>/ws/…`, proxied to the right worker.
- **Agent logins: shared global, opt-out per project.** A global
  `sbox_agents_vol` (claude/codex logins) is mounted into every worker so you log
  in once; a project sets `isolated_agents = true` in `.slopbox.toml` to get its
  own.
- **Auto port mapping** for `.slopbox.toml` `ports`; discovered ports get a
  toggle in the UI to map. Mapped to localhost and/or tailnet.
- **No registry / no published image.** GHA only goreleases `sb` → Homebrew. The
  image is built locally from embedded assets, tagged `sb-<ver>`.
- **Global state in `~/.config/sbox`** (`~/Library/Application Support/sbox` on
  mac): Tailscale state, auth/passkey store, the worker registry, SSH keys. The
  *only* thing in a project dir is an optional committed `.slopbox.toml` — no
  gitignore sprawl.
- **Minimal surface:** `sb`, `sb --no-auth`. Config via `.slopbox.toml` + a global
  config; drop the env-var surface for users.

## Custom base image (bring your own environment)

The embedded Dockerfile is a **template**: `FROM {{base}}` + a lean slopbox
**overlay**. A project picks its own environment; slopbox augments it.

- `.slopbox.toml`: `base = "node:20"` (use an image) **or** `dockerfile =
  "Dockerfile.dev"` (slopbox builds it first, then layers on top). Default base =
  slopbox's own `ubuntu:24.04` + full tooling.
- The **overlay injects only the must-haves**: the worker binary, the entrypoint,
  a non-root `ubuntu` user + sudo, `git` (+ `safe.directory '*'`), `openssh-server`,
  `ca-certificates`, `curl`. Everything heavy — postgres, node, code-server,
  mise tools — is **opt-in (config flags) or lazy (first-run download)**, so the
  image stays small and your base provides the real toolchain.
- Constraint: the overlay uses `apt` + `useradd`, so bases must be
  **Debian/Ubuntu-family** (covers nearly all dev bases). Alpine/distroless would
  need a separate overlay — out of scope for now.

## `sb` is idempotent + a coordinator daemon

- First `sb` in any dir: build image if needed → start the coordinator → register
  cwd as a worker → open the UI.
- `cd other && sb`: detect the running coordinator → register a *new* worker →
  open the same UI. One coordinator, many workers, one port.

## What survives from v1

~80%. The worker is today's container app (worktrees, PTY sessions, ring-buffer
WS bridge + reconnect, `/p/`, ports detection, code-server `/edit`, postgres,
sshd, the passkey/auth code can move to the coordinator). New code is the host
coordinator: image builder, worker lifecycle, UI aggregation/proxy, SSH
port-mapper, `tsnet`.

## Phased plan

1. **Coordinator skeleton + single-worker proxy.** `cmd/sb`: ensure image, run one
   worker (loopback-published API port), serve the UI on one port reverse-proxying
   `/w/<id>/…`. UI gains a (single-project) project dimension. Proves the model.
2. **Multi-project.** Register N workers; project sidebar; one running coordinator;
   `cd && sb` attaches.
3. **Dynamic SSH port-mapper.** Coordinator ⇄ worker SSH; auto-map `ports`,
   toggle discovered; bind localhost (+ tailnet later).
4. **Tailscale via tsnet.** Expose the UI + mapped ports on the tailnet; state in
   the global dir.
5. **Embed + ship.** Embed Dockerfile + worker binaries + assets in `sb`; build
   `sb-<ver>` locally; goreleaser + Homebrew; drop the registry image + CI build.
6. **Shared agents + opt-out;** auth moves to the coordinator; retire per-box UI.
