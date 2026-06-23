# shellraiser — Design

A single Docker image for sandboxed "vibe coding." You point it at a project
repo, run it, and get a **web** front end for managing git worktrees and the
coding sessions running on them (Claude Code, Codex, shells, terminal editors).
Everything runs *inside* the container, so agents can run in danger mode — the
container, not your machine, is the blast radius. The same browser UI works
locally and, through a tunnel, from an iPad on the couch.

> This replaces the earlier "Vibe Shell" host-side TUI design. The pivot:
> instead of a cross-platform Go TUI multiplexed over SSH, shellraiser is one
> immutable container running a Go **web** app, with all state in bind mounts.
> See [idea.md](idea.md) for the product brief and [STATUS.md](STATUS.md) for
> what's implemented today.

---

## 1. Requirements

| # | Requirement | Where |
|---|-------------|-------|
| R1 | One immutable Docker image; `docker run` with a repo + a state mount | §7 |
| R2 | Web UI to create / attach / remove git worktrees | §6 |
| R3 | Launch & multiplex Claude Code, Codex, shells, editors per worktree | §3 |
| R4 | Top-notch terminal emulator in the browser | §3, §5 |
| R5 | Detect when an agent is "working" vs done; ding on done | §4 |
| R6 | Semi-persistent: tools, logins, keys, worktrees survive restarts | §7 |
| R7 | Remote access with auth (token/magic-link), optional tunnel | §8 |
| R8 | Port passthrough for dev servers | §6.3 |
| R9 | Optional host docker access without docker-in-docker | §9 |
| R10 | zsh + starship; vim, helix, Fresh; mise; agents preinstalled | §7.1 |
| R11 | Multi-arch image built + published by CI | §10 |
| R12 | Mobile-responsive UI | §5.3 |

---

## 2. Component map

```
cmd/worker            entrypoint: flags/env → server.New → Run
internal/server        HTTP API, websocket terminal bridge, SSE, auth, static
internal/session       PTY-backed sessions, ring buffer, activity detection
internal/worktree      thin wrapper over `git worktree`
web/                   embedded UI (index.html + app.js), Tailwind + xterm.js
docker/                entrypoint.sh + skel/.zshrc seeded into the home mount
Dockerfile             multi-stage: cross-compile Go → Ubuntu runtime image
run.sh                 dogfood launcher (build + run against a local repo)
.github/workflows      multi-arch build → GHCR
```

The Go binary embeds the web assets (`//go:embed`), so the running artifact is a
single static file. No Node or build step ships in the image for the UI;
Tailwind and xterm.js currently load from CDNs (see §11).

---

## 3. Process & terminal model

The hard part of any multiplexer is hosting arbitrary interactive TUIs (Claude,
vim, a shell) and rendering them to one or more attached clients. shellraiser owns
each process's **PTY** and bridges raw bytes to the browser's xterm.js — the
same approach tmux and Zellij use (we own the PTY; we don't reparent).

A **Session** (`internal/session`) is:
- an `exec.Cmd` started on a PTY via `creack/pty`, with `TERM=xterm-256color`;
- a **ring buffer** (256 KB) of recent output, so a (re)attaching client gets
  immediate scrollback instead of a blank screen;
- a set of **subscribers** (one per attached websocket) fed live output;
- an **activity/state** record (see §4).

Data flow per session:

```
process ──PTY──▶ readLoop ──▶ ring buffer (replay on attach)
                         └──▶ each subscriber chan ──▶ websocket ──▶ xterm.js
xterm.js ──▶ websocket ──▶ Session.Write ──▶ PTY ──▶ process   (keystrokes)
xterm fit ──▶ {resize,cols,rows} ──▶ Session.Resize ──▶ pty.Setsize
```

Subscriber channels are buffered and **drop on overflow** rather than stall the
process — a slow client degrades its own render, never the agent. (A future
upgrade resyncs from the ring on overflow; see §11.)

**Lifetime.** Sessions live in the shellraiser process. They survive client
disconnect/reconnect (ring + re-subscribe). They do **not** survive a restart of
the shellraiser process itself — the planned fix is tmux-backed sessions so a
process restart can re-attach. Today the container staying up *is* the
persistence boundary.

---

## 4. Activity detection & ding

Goal: light up "working" while an agent streams output, and **ding** when it
finishes a unit of work and goes quiet waiting on you.

Per-session state machine (`session.go:monitor`, 300 ms tick):

```
            output within 1s
   idle ───────────────────────▶ running        (runningSince = now)
            no output for 1s
running ───────────────────────▶ idle           ding if:
                                                   kind ∈ {claude, codex}
                                                   AND running lasted ≥ 2s
process exits ───────────────▶ exited (exitCode)
```

Transitions are emitted as `Event`s, fanned out over SSE (`/api/events`). The UI
turns `state` into a coloured dot (green pulsing = running, grey = idle, red =
exited) on every tab and sidebar row, and turns `ding=true` into a WebAudio
two-tone chime plus a title-bar flash.

This is deliberately a **heuristic**: "went quiet after working ≥2s" can't
distinguish "done" from "waiting for input," but both are exactly the moment you
want the ding. Tightening it would mean parsing agent-specific prompt markers.

---

## 5. Web UI

### 5.1 Layout
- **Sidebar:** worktrees, each expandable to its live sessions (coloured status
  dots); a "New Worktree" action; a ports panel.
- **Toolbar:** active-worktree context + launch buttons (claude · codex · shell
  · editor) that spawn a session in the selected worktree's directory.
- **Tab strip + terminal area:** one xterm.js instance per open session, kept
  alive and toggled by visibility so background sessions keep rendering.

Styling is Tailwind (dark "ink" palette, slop-purple accent) chosen to match the
reference superset/conductor aesthetic.

### 5.2 Live updates
- `/api/events` (SSE) → session state dots + ding, no polling.
- `/api/ports` polled every 5 s → clickable port chips.
- `/ws/term/{id}` (websocket) → per-terminal byte stream.

### 5.3 Mobile
The sidebar is `static` on ≥ md and a fixed slide-in **drawer** below md,
toggled by a hamburger button with a tap-to-dismiss backdrop; it auto-closes
after you pick a worktree / open a session. Launch buttons collapse to icons on
narrow screens. xterm refits on resize. The iPad/phone case is first-class.

---

## 6. Worktrees

`internal/worktree` shells out to git and parses `--porcelain`:
- **List** — every registered worktree (path, branch/detached, HEAD, main flag).
- **Add** — new branch off HEAD/base, or check out an existing branch.
- **Remove** — delete from disk + prune.

### 6.1 Storage
Worktrees live under the persistent home mount
(`/home/ubuntu/worktrees/<name>`, via `SHELLRAISER_WORKTREES`), **not** inside the
`/work` bind mount. This keeps the project checkout clean and lets worktrees
persist with the rest of your state while still referencing the repo's `.git`
at `/work`.

### 6.2 Sessions are worktree-scoped
A launched session's working directory is the selected worktree path, so claude
in worktree A and claude in worktree B are fully isolated checkouts.

### 6.3 Ports
`/api/ports` detects LISTEN sockets (`ss` on Linux, `lsof` on macOS dev hosts)
and the UI renders them as links. Actually reaching a dev server from your
laptop is normal Docker `-p` publishing (or the tunnel); detection is discovery.

---

## 7. Image, persistence & user model

### 7.1 What's in the base image
Ubuntu 24.04 + git, tmux, zsh, **starship**, vim, **helix**, **Fresh**
(getfresh.dev), **mise**, Node, **Claude Code** + **Codex** (danger mode), a
static **docker client**, and the shellraiser binary. Built in two stages: a
`golang` stage cross-compiles the static binary for the target arch; the runtime
stage installs tooling and copies the binary in.

### 7.2 Immutability & the home-shadow rule
The image is immutable and rebuilt by CI. Everything *you* add at runtime lands
in the **persistent home mount** (`/home/ubuntu`): mise installs, the brew
prefix, agent logins, keys, history, config.

Critical subtlety: a bind/volume mount over `/home/ubuntu` **shadows** whatever
the image baked there. So:
- **Binaries** install to `/usr/local/bin` or via apt (system paths), never home.
- **Dotfiles** are kept in `/etc/skel` and **seeded** into the home mount by the
  entrypoint on first run (when missing), so an empty bind mount still gets a
  working `.zshrc` (mise activate, starship init, brew shellenv).

### 7.3 User
Runs as the stock non-root **`ubuntu`** user (uid 1000) with passwordless sudo
and zsh as its shell. The entrypoint starts as root only to fix mount ownership
and seed dotfiles, then `gosu ubuntu` drops privileges for the app and every
session. uid 1000 keeps bind-mount ownership tidy on Linux hosts.

### 7.4 Layering summary
| Layer | Mount | Mutable | Holds |
|-------|-------|---------|-------|
| Base image | — | no (CI rebuild) | OS, tools, agents, shellraiser binary |
| Home | `/home/ubuntu` | yes | mise/brew installs, logins, keys, worktrees |
| Project | `/work` | yes | the repo + its `.git` |

---

## 8. Auth & remote access

### 8.1 Auth — passkeys (WebAuthn)
The box is gated by passkeys (`internal/auth`, go-webauthn). On first boot a
**bootstrap code** is generated (persisted in the home mount, printed to the
logs). You use it once to register a passkey; after that you sign in with the
passkey and can add more while signed in.

**Per-origin RP IDs.** A WebAuthn credential is bound to one RP ID (registrable
domain). shellraiser is reachable via localhost *and* tunnel hostnames, so each
credential is stored with the RP ID it was registered under, discovered from the
request `Host`. Login offers only the credentials matching the current origin.
`rp_id` pins a single registrable domain instead (e.g. an apex, to share one
passkey across subdomains).

`SHELLRAISER_TOKEN` is an optional bearer-token fallback for automation; `--no-auth`
disables auth for trusted local/test use (it's what the Playwright suite drives).

### 8.2 Attack surface — minimal, default-deny
- **One listener.** Only shellraiser's port is published. pgweb (`:8081`) and
  code-server (`:8082`) bind `127.0.0.1` *inside* the container — unreachable
  except through shellraiser's gated `/db` and `/edit` proxies.
- **Default-deny routing.** `publicPath` is a strict allowlist: only the login
  SPA shell (`/`, `/app.js`, `/favicon.ico`) and `/api/auth/*` are reachable
  unauthenticated. Every data API, the terminal websocket, and both proxies
  require a valid session — a new route is private by default.
- **CSWSH protection.** Websocket upgrades verify the `Origin` matches `Host`.
- **Rate limiting.** Bad bootstrap-code attempts lock out after 5 tries; the code
  itself is 12 chars over a 32-symbol alphabet (~10^18 space).
- **Cookies.** Session + ceremony cookies are HttpOnly, SameSite=Lax, Secure
  under HTTPS; tokens compared in constant time.

### 8.2 External exposure (env-selected)
The entrypoint opens a tunnel based on which env vars are present:
- `CLOUDFLARE_TUNNEL_TOKEN` → `cloudflared`
- `GATECRASH_TOKEN` → gate-crash (github.com/JClement/gate-crash)
- neither → local port only (bring your own SSH tunnel / Tailscale / proxy)

(The wiring exists; the tunnel binaries aren't installed in the image yet — §11.)

---

## 9. Docker access (no DinD)

When a session needs to drive containers, pass through the host docker socket
(`-v /var/run/docker.sock:...`, or `./run.sh --docker`). The image ships only the
docker **client**; there is no daemon and no docker-in-docker. The entrypoint
reads the socket's gid and adds `ubuntu` to a matching group so it works without
root. This is the standard, lighter, safer-by-default choice vs. true DinD; DinD
(privileged, nested daemon) remains a possible opt-in later if real isolation
from the host daemon is ever needed.

---

## 10. Build & release

- `Dockerfile` is multi-arch aware (`TARGETOS`/`TARGETARCH` cross-compile; static
  docker CLI selected by `uname -m`).
- `.github/workflows/build.yml` builds **linux/amd64 + linux/arm64** (Apple
  Silicon laptops, amd64 servers) and pushes to **ghcr.io/jclement/shellraiser**:
  `:edge` on main, `:vX.Y.Z`/`:latest` on tags, plus a **weekly rebuild** so the
  base tooling (apt, mise, agents) stays fresh.
- `run.sh` is the local dogfood path: build `shellraiser:local`, run against a repo,
  state under `~/.shellraiser/<name>/home`, print the magic link.

---

## 11. Known gaps / roadmap

- **Tunnel binaries** — install cloudflared / gate-crash in the image so §8.2
  works out of the box.
- **Session persistence across shellraiser restarts** — back sessions with tmux.
- **Overflow resync** — on subscriber drop, replay the ring to resync xterm.
- **Vendored assets** — embed Tailwind + xterm instead of CDN for an offline,
  CSP-tight image.
- **Worktree UX** — real dialog (base-branch picker, attach-existing) over the
  current `prompt()`.
- **GPG/SSH agent forwarding** — forward host agent sockets; fall back to a
  container-owned key in the home mount.
- **Homebrew bootstrap** — lazily install brew into the home mount on first run.
- **Per-project config** — custom session launchers + auto-start rules from a
  file in the repo.
- **Image size** — 2.6 GB; slim build-essential / multi-stage the tooling.
</content>
