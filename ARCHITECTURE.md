# sbox — architecture (v2: coordinator + workers)

A rethink of slopbox: instead of one box = one container = one UI/port, a single
host **coordinator** (`sb`) fronts many per-project **worker** containers behind
**one UI and one port**.

```
            ┌─────────────────────── your machine ───────────────────────┐
            │                                                             │
   browser ─┼─▶ sb (coordinator, host binary)                            │
 tailnet ───┼─▶   • one UI (unified: all projects/worktrees/sessions)     │
            │     • one passkey auth (enforced before every proxy hop)    │
            │     • builds sb-<hash> image from embedded Dockerfile        │
            │     • per-project worker lifecycle + resource caps           │
            │     • dynamic port mapping (host/tailnet ⇄ worker, via SSH) │
            │     • optional Tailscale via tsnet (no host tailscale)      │
            │        │            │                                       │
            │   proxy│api+ws  ssh │-L tunnels                             │
            │        ▼            ▼                                       │
            │   ┌─ sb_p1 ───────┐  ┌─ sb_p2 ───────┐   (worker containers)│
            │   │ worker API    │  │ worker API    │   = today's app,     │
            │   │ PTY sessions  │  │ PTY sessions  │     headless backend │
            │   │ /p/ proxy     │  │ /p/ proxy     │   own docker network │
            │   │ (lazy edit/db)│  │ (lazy edit/db)│   loopback-only ports│
            │   └───────────────┘  └───────────────┘                      │
            └─────────────────────────────────────────────────────────────┘
```

This document is the **build contract**. It supersedes DESIGN.md (v1). It was
finalized from a 20-agent planning review (16 design dimensions + 4 adversarial
stress teams: security, cross-platform, density, state). Verdict from both stress
teams: *the bones are right; the work is hardening + lifecycle, not redesign.*

---

## The two binaries

**`sb` — host binary** (mac/linux × amd64/arm64; Windows via WSL2; Homebrew;
versioned). One static pure-Go binary that **embeds**: the Dockerfile template,
both linux worker binaries (amd64+arm64), the entrypoint, and the web UI assets.
Runs as the normal host user, never root. Responsibilities:

- **CLI** (muscle-memory verbs): bare `sb` (ensure coordinator + register cwd +
  open UI), `sb --no-auth`, plus `sb ls`/`status`, `sb sh`/`ish`, `sb stop`,
  `sb nuke`, `sb logs`, `sb down`, `sb doctor`, `sb login`, `sb rebuild`.
- **Image**: build the `sb-<hash>` image locally from embedded assets if missing —
  no registry. (`§ Image build`.)
- **Coordinator daemon**: serve the unified UI on **one** port; reverse-proxy each
  worker under `/w/<id>/…`; aggregate into one view; own lifecycle, port-mapping,
  and tsnet.

**Worker — container** = today's app, demoted to an untrusted headless backend
(API + PTY + `/p/` + lazy code-server/postgres). UI and auth move to the
coordinator. One container `sb_<id>` + one named volume `sb_<id>_vol` per project.
Publishes **nothing public** — only `127.0.0.1:0:7000` (API) and `127.0.0.1:0:22`
(sshd), both loopback, both ephemeral host ports discovered via `docker inspect`.

**~80% of v1 survives** as the worker: worktrees, PTY sessions, ring-buffer WS
bridge + reconnect, `/p/`, port discovery, code-server `/edit`, postgres, sshd,
metaStore colors. New code is the host coordinator.

---

## Hard invariants (CI-linted where possible)

1. **Loopback-published only.** The coordinator reaches a worker ONLY via
   host-published `127.0.0.1` ports (API + sshd) and the `/w/<id>/` proxy.
   **Container IPs (`NetworkSettings.IPAddress`) are forbidden** — unreachable from
   the host on Docker Desktop (Mac/Windows run containers in a VM). This is the one
   routing primitive identical on all platforms. *(grep-fail CI guard.)*
2. **Project source is the only bind mount** (`/work`). Everything stateful
   (home, postgres, mise, code-server, agent state) is a **named volume** —
   bind-mounting home breaks uid/perms/perf on Docker Desktop.
3. **Auth is enforced at the coordinator before every proxy hop** — HTTP *and* the
   WS upgrade — with `<id>` validated against the registry. Workers never serve a
   public origin.
4. **Docker is the source of truth.** The registry is reconciled from `docker ps`
   by `slopbox.*` labels on boot + timer; `registry.json` is a flock-guarded hint
   cache, not authoritative. A coordinator crash / `brew upgrade` re-adopts running
   workers with zero data loss.
5. **Workers are untrusted** (danger-mode agents = container-root). The boundary is
   the container + its namespaces. Each worker gets its **own docker network**.

---

## Decisions (locked + adopted defaults)

Locked by the user earlier:
- **Unified UI.** One sidebar: projects → worktrees → sessions; one cross-project
  view. All calls go to `/w/<id>/api/…` and `/w/<id>/ws/…`.
- **Shared agent logins, opt-out per project** via `isolated_agents = true`.
- **Auto port mapping** for `.slopbox.toml` `ports`; discovered ports get a UI
  toggle. Bound to localhost and/or tailnet.
- **No registry / no published image.** GHA only goreleases `sb` → Homebrew.
- **Global state in `~/.config/sbox`** (`os.UserConfigDir()`; mac:
  `~/Library/Application Support/sbox`). The *only* per-project file is an optional
  committed `.slopbox.toml` — no gitignore sprawl.
- **Minimal surface:** `sb`, `sb --no-auth`; config via files, not env vars.

Adopted defaults (the 7 synthesis forks — recommended options taken, overridable):
1. **`isolated_agents`** stays **shared-by-default** (convenience); UI toggle is
   prominent + documented as an account-credential trust expansion.
2. **Postgres defaults OFF** (lean v2), opt-in per project. *(Breaking vs v1; noted
   in migration.)*
3. **Idle auto-stop ON at 30m** grace, `keep_warm` opt-out; never reaps an active
   session/forward; resume 1–3s.
4. **Worker fate on `sb down`/coordinator exit: keep running** (instant re-adopt);
   `sb down` explicitly stops all.
5. **Tailnet trust:** passkey **always required**, even on tailnet; refuse
   `--no-auth` + tailnet together. Tailscale identity is a second factor, not a
   replacement.
6. **Apple Silicon default arch: native arm64**, per-project override for parity.
7. **Reboot survival:** double-fork (survives terminal close) is default;
   `sb service install` opt-in for a launchd/systemd unit.

---

## Security hardening (ship-blockers before any multi-worker phase hits a tailnet)

- **Per-worker docker network** `sb_net_<id>` (not the default bridge) so a
  danger-mode worker can't port-scan siblings. `sb doctor` asserts nothing is on
  the default bridge.
- **Worker API token.** Coordinator injects `SLOPBOX_WORKER_TOKEN` (32 random
  bytes, regenerated per start); worker rejects any request lacking it. Loopback is
  not auth on a multi-process host.
- **Hardened worker sshd**: `AllowTcpForwarding local`, `PermitOpen 127.0.0.1:*
  ::1:*`, `GatewayPorts no`, `AllowAgentForwarding no`, `PermitTunnel no`.
  Coordinator uses only `-L`, never `-R`. One coordinator keypair in the global dir
  (0600), pubkey injected per worker.
- **Resource caps** on every `docker run`: `--memory=2g --pids-limit=512
  --cpu-shares=1024` (overridable in `.slopbox.toml`). Inspect `State.OOMKilled`.
- **Reserved-port denylist** (5432/8081/8082/7000/22): `/p/` and the port-mapper
  refuse them; postgres/pgweb/code-server hard-blocked from tailnet exposure.
- **Docker socket** off by default; gated per-project (`docker_socket = true`) with
  a loud one-time warning. *(Host-takeover grant under a hostile agent.)*
- **Global dir** 0700, files 0600; coordinator refuses to start if looser.
- **RP-ID pinned** to an enumerated origin set (localhost + tsnet MagicDNS), never
  derived from `Host`.

---

## Custom base image (bring your own environment)

The embedded Dockerfile is a **template**: `FROM {{base}}` + a lean slopbox
overlay. A project picks its own environment; slopbox augments it.

- `.slopbox.toml`: `base = "node:20"` (use an image) **or** `dockerfile =
  "Dockerfile.dev"` (slopbox builds it first, then layers on top). Mutually
  exclusive. Default base = slopbox's own `ubuntu:24.04` + full tooling.
- **Overlay must-haves** (always baked, ~6 pkgs / 50–80MB): worker binary,
  entrypoint, `ubuntu` user + sudo, `git` (+ `safe.directory '*'`),
  `openssh-server`, `ca-certificates`, `curl`, `gosu`. COPY the worker binary
  **last** so a worker bump rebuilds only the final tiny layer.
- **Lazy / opt-in** (not in the overlay): postgres, pgweb, code-server, node/agents,
  mise, helix, in-container tailscale, docker CLI. The base provides the real
  toolchain; heavy bits download on first use into the home volume.
- Constraint: overlay uses `apt` + `useradd`, so bases must be **Debian/Ubuntu
  family**. Probe `command -v apt-get` and fail friendly otherwise.

## Image build

Stop building the worker binary in Docker. Cross-compile linux amd64+arm64 at
release, `go:embed` both + template + entrypoint into `sb`; at runtime materialize
a tiny build context (no Go toolchain in the image). Tag by **content hash**
`sb-<HASH>` over (resolved base digest + overlay bytes + worker binary + feature
flags + engine arch). Build via `docker build` + BuildKit `--progress=plain`,
streamed into the UI's existing ANSI ring-buffer pane. Auto-rebuild when the hash
changes; `sb rebuild [--no-cache]` forces it. Replace the `imageExists` fatal with
build-then-start; detect docker-missing/not-running first.

## Density

An agent-only worker should idle at ~30–60MB RSS. Flip postgres/pgweb/code-server
from eager to lazy/opt-in (postgres default OFF; code-server starts on first
`/edit`). Per-worker caps (above). Idle auto-stop driven by the worker's existing
`/api/sessions` state + proxy activity: `docker stop` (not `pause` — pause keeps
RAM resident) after 30m, never while a PTY/WS/agent session or live SSH forward is
active; lazy resume with a "waking" interstitial. Serialize cold starts with a 2–3
semaphore to avoid a boot storm.

## State & layout (`~/.config/sbox`, 0700)

```
config.toml          host/coordinator knobs (port, auth, rp_id, tailnet, defaults)
registry.json        hint cache of workers (flock via registry.lock); reconciled from docker
coord.lock           single-instance flock; holds pid+port
sb.sock              unix-socket control plane (0600) — CLI ⇄ daemon
auth/store.json      passkey/WebAuthn credentials (moved from the worker; 0600)
ssh/coordinator_ed25519[.pub]   coordinator SSH keypair (0600), pubkey injected per worker
tsnet/               tsnet.Server.Dir (Tailscale node state)
secrets/<id>.env     per-project secrets (0600, --env-file); never in the repo
image/build.json     {tag, base, overlay+worker hashes, image id, built_at}
workers/<id>/        worker.json + logs
```

Per-project: one home volume `sb_<id>_vol` → `/home/ubuntu` (postgres, mise,
dotfiles, worktrees) — the single stateful + backup unit. `id` =
`.slopbox.toml id` else a stable hash of the absolute path (basename collides
across same-named repos; validated + de-duped against the registry). One global
`sbox_agents_vol` for shared logins (dropped when `isolated_agents`).

## Config (two-file split)

- **Project `.slopbox.toml`** (committed, worker description): `id`, `base`/
  `dockerfile`, `ports`, `postgres`, `code`, `isolated_agents`, `docker_socket`,
  `keep_warm`, `[env]`, `[commands]`, resource overrides.
- **Global `config.toml`** (host): `port`, `auth`/`no_auth`, `rp_id`, `tailnet`,
  default base, idle grace, per-service defaults.
- Precedence: flags → global → project → defaults. Strict parsing (reject unknown
  top-level keys; warn inside known tables). Drop the user env-var surface and
  `.slopbox.local.toml`.

## Daemon & control plane

First `sb` double-forks a detached coordinator (survives terminal close);
single-instance via `coord.lock` flock + health check on fixed `127.0.0.1:7700`.
Every later `sb` is a thin client that registers cwd and returns the URL the
instant the coordinator listens. Control plane is a **unix socket** `sb.sock`
(0600) — filesystem-permission-gated, no extra TCP port, immune to browser
CSRF/DNS-rebinding. A newer `sb` binary supersedes an older running coordinator via
a graceful `/shutdown` that leaves workers running. `sb doctor` re-runs the
startup preflight (docker, image, perms, coordinator, ports, tsnet, passkeys) as a
pass/fail checklist with fix-its.

## Agent logins (avoid the corruption trap)

Do **not** mount the whole `~/.claude`/`~/.codex` shared rw — `~/.claude.json` is a
monolithic file rewritten almost every turn with no cross-process lock; two workers
truncate it and log you out everywhere. Instead: mount `sbox_agents_vol:/agents:ro`
(credential files only), and relocate all hot state per-worker via
`CLAUDE_CONFIG_DIR`/`CODEX_HOME` into `sb_<id>_vol`. Only writers: a single-writer
`sb login` (one throwaway rw container) and a coordinator-side token-refresh
daemon. Store **only** agent creds there — never git/cloud/ssh secrets.

## Cross-platform

SSH-forwarding + loopback ports is the *only* routing path. `os.UserConfigDir()`
for the global dir. Select worker arch from the **docker engine** arch (`docker
info`), not `runtime.GOARCH` (Apple Silicon can run emulated amd64). Ship Windows
as **install-inside-WSL2** running the linux binary — defer native windows/amd64
(path translation + loopback-vs-VM unsolved). Detect engine (Docker Desktop /
OrbStack / Colima) and warn if the project path is outside the file-sharing root.

---

## Phased build plan

1. ✅ **Coordinator skeleton + single-worker proxy.** `cmd/sb`: ensure image, run
   one worker (loopback API port), serve UI on one port. *(Done — commit 3372d6c.)*
2. **Coordinator core + unified UI.** Registry (docker-labels source of truth);
   multi-worker `/w/<id>/` proxy; hardened `ensureWorker` (per-worker network,
   worker token, resource caps); worker-token enforcement; coordinator auth before
   proxy (HTTP+WS); project→worktree→session sidebar; cross-project agents view;
   container controls (stop/restart/nuke) in UI. Daemon (double-fork, unix socket,
   reconcile); `sb` subcommands; density (lazy services, idle auto-stop).
3. **Dynamic SSH port-mapper.** Persistent `x/crypto/ssh` client per worker;
   `net.Listen` per mapping; auto-map `ports`, toggle discovered; loopback bind
   (+ tailnet later); reserved denylist; hardened worker sshd.
4. **Tailscale via tsnet.** Expose UI + mapped ports on the tailnet; state in the
   global dir; passkey still required; refuse `--no-auth` + tailnet.
5. **Embed + ship.** Embed Dockerfile template + worker binaries + assets; build
   `sb-<hash>` locally; custom base image; goreleaser + Homebrew; drop the registry
   image + CI image build.
6. **Shared agents + opt-out.** Creds-only `:ro` mount + per-worker
   `CLAUDE_CONFIG_DIR`; `sb login` single-writer; auth fully on the coordinator;
   retire the per-box UI.

Each phase: implemented, tested (docker + curl + Playwright), documented, committed.
Nothing half-built.
