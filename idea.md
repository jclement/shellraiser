# shellraiser

A single Docker image for sandboxed "vibe coding." You point it at a project
repo, run it, and get a web-based front end for managing git worktrees and
running coding agents (Claude Code, Codex), terminal editors, and shells —
all inside an isolated container you can also reach remotely.

The whole point: spin up one box (locally or on a server somewhere), do all your
agentic development *inside* it, and let the agents run in danger mode because
the container — not your real machine — is the blast radius.

> **Status:** the Go web app + image are built. See [STATUS.md](STATUS.md) for
> what works today and what's still stubbed.

## Quickstart (dogfood)

`./run.sh` builds the image and runs it against a local repo, with state bind-
mounted to `~/.shellraiser/<name>/home` so it persists:

```
./run.sh                                  # this repo, port 7000
./run.sh --name dev --port 7100 ~/dev/barreleye
./run.sh --rebuild                        # force-rebuild the image first
./run.sh --docker                         # pass through the host docker socket
./run.sh --pull                           # use ghcr.io/jclement/shellraiser:latest
```

It prints the magic link (`http://localhost:<port>/?t=<token>`) on start.

## Shape of the thing

Not a host-side app. It's **one static Docker image** with a Go web app baked
in. No per-host installer, no TUI. You get at everything through the browser.

```
docker run \
  -v /path/to/project:/work \          # the repo you're hacking on
  -v shellraiser-home:/home/ubuntu \         # persistent state (home dir)
  -p 7000:7000 \                       # web UI
  ghcr.io/jclement/shellraiser:latest
```

- **`/work`** — bind mount of the project repo (e.g. Barreleye). Worktrees are
  created off this.
- **persistent home** — bind mount (or named volume) for `/home/ubuntu`. Holds
  installed tools, agent logins, shell history, keys, config. Survives restarts
  and image upgrades.
- **port passthrough** — anything the dev servers inside expose, you forward
  with normal `-p` flags. Docker handles it.
- **docker access (optional)** — pass through the host docker socket
  (`-v /var/run/docker.sock:/var/run/docker.sock`, or `./run.sh --docker`) when a
  session needs to drive containers. The image ships only the docker *client*;
  there's no docker-in-docker and no daemon inside. The entrypoint adds `ubuntu` to
  the socket's group so it works without running as root.

**Worktree storage:** worktrees live in the persistent home mount
(`/home/ubuntu/worktrees/<name>`, set via `SHELLRAISER_WORKTREES`), not inside `/work`.
That keeps the project bind mount clean and lets worktrees persist with the rest
of your state; they still reference the repo's `.git` at `/work`.

A `docker-compose.yml` is the expected day-to-day entry point: `docker compose up`
on a server and you're in business.

## Base image & tooling

Ubuntu base. Ships out of the box with:

- **mise** — the dev-tool/version manager; primary way to install languages,
  runtimes, and dev CLIs at runtime. Its install dir lives in home, so installs
  persist.
- **Homebrew (Linuxbrew)** — for the long tail of CLI apps mise doesn't package
  (`ripgrep`, `fd`, `jq`, `gh`, …). Brew is non-root and prefix-relocatable, so
  its installs land in the persistent home mount too. **Caveat:** keep brew at
  its *standard* prefix (`/home/linuxbrew/.linuxbrew`) and put *that path* on the
  volume — relocating to a non-standard prefix forces source builds instead of
  prebuilt bottles. Only brew's runtime + build deps are baked into the base.
- **git** — including worktree support, the core primitive here.
- **vim** and **helix** — terminal editors.
- **Claude Code** and **Codex** — preinstalled, configured to run in danger /
  skip-permissions mode since the container is the sandbox.
- A web terminal stack (PTY + xterm.js or equivalent) for shells in the browser.

On first run you finish setup interactively: `mise` whatever extra tools the
project needs, log into Claude/Codex, etc. Because home is a bind mount, that
state persists — you do it once.

## Web UI

Single Go binary serves the whole front end. Capabilities:

### Worktrees
- Create a new worktree off the project repo (new branch, existing branch, or
  detached).
- List/attach to existing worktrees.
- Open a worktree (drops you into shells/agents scoped to it).
- **Remove a worktree from disk** when you're done with it.

### Sessions inside a worktree
Predefined launchers, each running as its own persistent session (tmux-backed
under the hood so they survive disconnects):
- **Claude Code** session
- **Codex** session
- **Terminal** (plain shell)
- **Editor** (vim / helix)
- Project-defined custom commands (from a config file in the repo).

Some launch automatically on worktree creation; others are manual. Configurable.

### Quality of life
- **Sounds** when a worker/agent session finishes, so you know when Claude/Codex
  is done and waiting on you.
- Navigate between worktrees and their running sessions from the browser.
- Live terminal streaming in the browser.

## Persistence model

The base image is **immutable** — anything you install at runtime lands in a
bind mount, never in the image. The two mounts that matter:
- the **project repo** (`/work`)
- the **home dir** (`/home/ubuntu`) — mise installs, the brew prefix, agent
  logins, keys, history, config.

The rule of thumb for what goes where:
- **Base image (immutable, root-owned, rebuilt by CI):** OS + apt packages, git,
  editors, the agents, mise, brew's runtime + build deps, the Go web app.
  Anything that needs root or writes to `/usr` can't persist via a home mount, so
  it belongs here.
- **Persistent home (yours, non-root):** everything you add later via mise/brew,
  plus all config, logins, and keys.

Upgrade = pull a new image, restart. State is untouched.

## Auth & remote access

The reason this exists: run it on a machine at home (or a VPS), then connect
from anywhere — iPad on the couch, laptop away from the house — and keep working
on all your sessions.

### Logging into the web UI
- By default, a **random password (or magic link)** is generated on first boot,
  stored in a root-readable file inside the container and printed to the
  startup logs. Override via config/env if you want a fixed credential.
- Security is a first-class concern: no unauthenticated access, ever.

### Exposing it externally (pick via env vars)
- **Cloudflare Tunnel** (`cloudflared`) — set the relevant env vars and it
  brings up a tunnel.
- **gate-crash** (`github.com/JClement/gate-crash`) — set an API key / config
  and it exposes the service.
- **neither** — local port only; you bring your own (SSH tunnel, Tailscale,
  reverse proxy, whatever).

Which one fires depends purely on which env vars are present.

## SSH / GPG signing

Want git commit signing and SSH to "just work" inside the box:
- **Preferred:** forward the host's SSH agent and/or GPG agent socket in via a
  bind mount, so the container borrows your real keys without ever holding them.
- **Acceptable fallback:** the container owns a dedicated SSH/GPG key, generated
  on first run and kept in the persistent home mount. Fine for a box that owns
  its own identity.

## Build & release

Repo: **github.com/jclement/shellraiser** → images at **ghcr.io/jclement/shellraiser**.

- The **`Dockerfile`** produces the image. No host binary to ship.
- **`.github/workflows/build.yml`** builds and pushes to GHCR:
  - push to `main` → `:edge` (+ `:sha`)
  - tag `v*` → `:vX.Y.Z`, `:X.Y`, `:latest`
  - **weekly schedule** → rebuild to refresh base tooling (apt, mise, agents)
  - manual dispatch
- Multi-arch **linux/amd64 + linux/arm64** (laptop is Apple Silicon, servers are
  amd64), with GHA layer caching.

## Security posture (summary)

- The container is the isolation boundary — agents run in danger mode *because*
  they can't touch the host.
- Host keys are forwarded, not copied, when possible.
- Web UI is always authenticated; credentials are random by default.
- External exposure is opt-in and goes through Cloudflare/gate-crash with TLS.
- Persistent state is yours (bind mounts); the image is throwaway.

## Open questions / TODO

- Web terminal multiplexer: tmux + ttyd-style PTY bridge, or a custom Go PTY
  layer? Needs to handle reconnect cleanly.
- Magic link vs. password as the default — decide and implement one.
- Exact mechanism for SSH/GPG agent forwarding into a container across macOS
  (host is darwin) — the agent socket forwarding story differs from Linux hosts.
- Per-project config file format (where worktrees live, custom session
  launchers, auto-start rules).
- Sound delivery: browser-side audio triggered by session-exit events over the
  websocket.
</content>
</invoke>
