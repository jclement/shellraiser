<h1 align="center">📦 shellraiser</h1>

<p align="center">
  A single host binary — <code>sr</code> — that fronts many per-project sandbox
  containers behind <strong>one UI and one port</strong>. <code>cd</code> into a
  repo, run <code>sr</code>, and manage git worktrees plus the coding agents
  (Claude Code, Codex), shells, and editors running on them. <code>cd</code> into
  another repo, run <code>sr</code> again — it joins the same coordinator.
</p>

<p align="center">
  <a href="#quickstart">Quickstart</a> ·
  <a href="#commands">Commands</a> ·
  <a href="#configuration">Configuration</a> ·
  <a href="#port-mapping">Port mapping</a> ·
  <a href="#how-it-works">How it works</a> ·
  <a href="#security">Security</a>
</p>

> Agents run in **danger mode** because the container — not your machine — is the
> blast radius. Each project is its own container, network, and volume; only the
> coordinator (running as you, on the host) holds auth and secrets.

---

## Quickstart

```bash
# one-time: build sr (cross-compiles + embeds the linux worker binaries)
mise run build         # → dist/sr     (or: brew install jclement/tap/sr)

cd ~/dev/project-a && sr        # builds the image on first run, opens the UI
cd ~/dev/project-b && sr        # joins the SAME coordinator — one UI, one port
```

The first `sr` builds the worker image locally from assets embedded in the binary
(no registry), starts a detached **coordinator** on `http://localhost:7700`,
registers the current repo as a **worker** container, and opens your browser.
Every later `sr` in any repo attaches to that one coordinator.

There is nothing to install in the image and nothing to `.gitignore` in your repo
— the only project file is an optional committed `.shellraiser.toml`.

## Commands

```bash
sr                # ensure the coordinator, register cwd, open the UI
sr --no-auth      # …without passkey auth (loopback-only; refused with --tailnet)
sr ls   / status  # color dashboard: coordinator + every project + ports
sr stop [id]      # stop a worker (all if omitted) — data kept
sr nuke  id       # remove a worker's container + volume + network (repo untouched)
sr logs  id       # follow a worker's container logs
sr login          # log into claude/codex once, shared across projects
sr down           # stop every worker and shut the coordinator down
sr doctor         # preflight: docker, embedded worker, base image, isolation
```

Workers **idle-stop** after 30 minutes with no running session and **wake on the
next request**, so a dozen registered projects cost almost nothing at rest.

## Configuration

A committed, optional `.shellraiser.toml` in the repo describes the **worker**:

```toml
id        = "myproj"          # identity (else the folder name); container = sr_<id>
base      = "node:20"         # bring your own base image (Debian/Ubuntu family)…
# dockerfile = "Dockerfile.dev"  # …or have shellraiser build yours first, then layer on top
postgres  = true              # opt in to postgres + the /db UI (default: off)
code      = true              # code-server at /edit (default: on, lazy-installed)
ports     = ["5173", "8000-8010"]   # auto-map these to host loopback on start
isolated_agents = true        # don't share the global claude/codex login

[[commands]]                  # custom one-click launchers (toml only)
name = "dev"
args = ["npm", "run", "dev"]
```

Host-wide knobs (UI port, auth) are flags/global config, not per-project. The
default base is shellraiser's own image (Ubuntu + zsh/starship, mise, helix, node,
the agents, postgres, tailscale); a custom `base`/`dockerfile` gets a lean
overlay (the worker binary, git, sshd, sudo, an `ubuntu` user) on top.

## Port mapping

Every project's declared `ports` are auto-forwarded to **host loopback** via an
SSH `-L` tunnel the moment the worker starts; discovered dev-server ports get a
one-click **map** toggle in the UI. Mapping binds `127.0.0.1` only (never
`0.0.0.0`) and works identically on macOS, Linux, and WSL2 — it's the one routing
primitive that crosses Docker Desktop's VM boundary. HTTP services are also
reachable through the in-UI `/p/<port>/` proxy (handy on an iPad).

## How it works

```
 browser ─▶ sr (coordinator, host binary, one port, one passkey login)
              • builds sr-<hash> images locally from embedded assets
              • reverse-proxies each worker under /w/<id>/ (token-injected)
              • SSH -L port-mapper · idle reaper · docker-label registry
                   │ api+ws            │ ssh -L
                   ▼                   ▼
        ┌ sr_project-a ┐     ┌ sr_project-b ┐   worker containers:
        │ worker API   │     │ worker API   │   today's app as a headless
        │ PTY sessions │     │ PTY sessions │   backend — own network,
        │ /p/ · sshd   │     │ /p/ · sshd   │   own volume, loopback-only
        └──────────────┘     └──────────────┘
```

Docker is the source of truth: the coordinator reconciles its registry from
container labels, so a crash or `brew upgrade` re-adopts running workers with zero
data loss. See [ARCHITECTURE.md](ARCHITECTURE.md) for the full design.

## Security

- **One front door.** Only the coordinator is reachable; it enforces passkey auth
  (WebAuthn) before every proxy hop — HTTP and websockets alike.
- **Untrusted workers.** Each worker is a danger-mode sandbox: its own docker
  network (no sibling reachability), `--memory`/`--pids` caps, a loopback-only API
  fenced by a per-worker token, and a hardened sshd (`AllowTcpForwarding local`,
  no agent/gateway/tunnel forwarding).
- **Secrets stay host-side** in `~/.config/shellraiser` (0700): passkey store, the
  coordinator SSH key, the worker registry. The shared agent-login volume is
  mounted **read-only** into workers; only `sr login` writes it.
- The docker socket is never mounted by default (it's a host-takeover grant under
  a hostile agent).

## Development

```bash
mise run build     # cross-compile workers + build dist/sr
mise run test      # go unit tests
mise run e2e       # Playwright end-to-end (worker UI + multi-project coordinator)
```
