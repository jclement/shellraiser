<h1 align="center">📦 slopbox</h1>

<p align="center">
  A single Docker image for sandboxed <em>vibe coding</em>. Point it at a repo,
  open the browser, and manage git worktrees + the coding agents running on them
  (Claude Code, Codex), shells, and editors — all inside one isolated container
  you can also reach from anywhere.
</p>

<p align="center">
  <a href="#quickstart">Quickstart</a> ·
  <a href="#what-you-get">What you get</a> ·
  <a href="#configuration">Configuration</a> ·
  <a href="#remote-access">Remote access</a> ·
  <a href="#how-it-works">How it works</a>
</p>

> Agents run in **danger mode** because the container — not your machine — is the
> blast radius. Everything mutable lives in one bind-mounted folder, so the image
> stays immutable and your state survives upgrades.

---

## Quickstart

**Manage boxes locally** with `slopbox.sh` (builds the image, runs per-repo):

```bash
./slopbox.sh start                # box for the current repo (port 7700)
./slopbox.sh start --port 7100 ~/dev/barreleye
./slopbox.sh list                 # all boxes: status, port, state size, project
./slopbox.sh ish                  # shell into the current dir's box
./slopbox.sh open                 # open the web UI
./slopbox.sh logs                 # follow logs
./slopbox.sh stop                 # stop (resume later with start)
./slopbox.sh nuke                 # remove a box AND its persistent state
```

When a box isn't named, the current dir's box is used — otherwise **fzf** lets
you pick (with a live preview). `start` flags: `--rebuild --port --docker --pull`.
It prints the URL + the passkey bootstrap code on start. (`./run.sh` still works
as an alias for `slopbox.sh start`.)

**Or docker compose:**

```bash
docker compose up -d && docker compose logs -f
```

**Or plain docker:**

```bash
docker run -d --name slopbox \
  -v "$PWD:/work" \                 # the repo
  -v slopbox-home:/home/ubuntu \    # persistent state
  -v slopbox-pg:/home/ubuntu/.local/share/slopbox/postgres \  # postgres data
  -p 7700:7000 \                    # host 7700, NOT 7000 (see note)
  ghcr.io/jclement/slopbox:latest
```

Open `http://localhost:7700/` and register a passkey with the bootstrap code from
the logs (`docker logs slopbox | grep -i bootstrap`).

> **macOS port note:** don't publish to host port **7000** (or 5000) — macOS
> **AirPlay Receiver** squats on them and returns a confusing `403 Access to
> localhost was denied`. `run.sh` defaults to 7700 for this reason.

---

## What you get

- **Worktrees** — create / attach / remove git worktrees from the browser, each
  showing live **git stats**: `+added / −deleted`, commits ahead of base,
  **dirty** flag, and **⇡/⇣ vs origin**.
- **Sessions** — launch **claude**, **codex**, a **shell**, or an **editor** in any
  worktree. Each is a real PTY rendered with a first-class terminal (xterm.js),
  surviving disconnects.
- **Busy indicators + ding** — sessions show a pulsing "working" halo while an
  agent streams output, and **chime** when it finishes a unit of work.
- **Ports, attributed to worktrees** — listening ports are detected and shown
  *under the worktree whose session opened them*, as clickable links.
- **Custom commands** — extra launcher buttons defined in `.slopbox.toml`.
- **Edit in VS Code** — click ✎ on a worktree to open it in **code-server** at
  `/edit` (built-in Git SCM + GitLens), behind the same auth.
- **Bundled Postgres** (`postgres/postgres`) with **pgweb at `/db`** in the UI.
- **Mobile-responsive** — drawer sidebar + hamburger; works great from an iPad.
- **Lovely logs** — colourful structured output; `docker compose logs -f` is a joy.

**Toolchain in the image:** zsh + starship, git, tmux, vim, helix, Fresh
(getfresh.dev), mise, Node, Claude Code, Codex, a static docker client,
cloudflared, the gatecrash client, Postgres + pgweb.

---

## Configuration

Settings resolve with this precedence (**highest first**):

```
environment variables  →  .slopbox.local.toml  →  .slopbox.toml  →  defaults
```

`.slopbox.toml` is checked in; `.slopbox.local.toml` is yours (gitignore it).
See [.slopbox.toml.example](.slopbox.toml.example).

| Setting | TOML key | Env var | Default |
|---|---|---|---|
| Listen address | `addr` | `SLOPBOX_ADDR` | `:7000` |
| Worktrees dir | `worktrees_dir` | `SLOPBOX_WORKTREES` | `/home/ubuntu/worktrees` |
| Web auth token | `token` | `SLOPBOX_TOKEN` | random (magic link) |
| Disable auth | `no_auth` | `SLOPBOX_NO_AUTH` | `false` |
| Postgres + pgweb | `postgres` | `SLOPBOX_POSTGRES` | `true` |
| Agent/editor/shell argv | `claude` `codex` `editor` `shell` | — | danger-mode defaults |

**Custom commands** (toml-only — env can't express them) become toolbar buttons:

```toml
[[commands]]
name = "dev"
icon = "▶"
args = ["mise", "run", "dev"]
```

---

## Authentication — passkeys

The web UI is gated by **passkeys (WebAuthn)**. On first boot a **bootstrap code**
is printed in the logs; you use it once to register a passkey, and after that you
sign in with the passkey. You can add more passkeys anytime while signed in.

Because a passkey is bound to one **RP ID** (registrable domain), slopbox stores
credentials **per hostname**, discovered from the request at registration time —
so you register one passkey for `localhost`, another for your tunnel hostname,
etc. (the "add a passkey" button is exactly this). Set `rp_id` in config to pin a
single registrable domain if you'd rather share one passkey across subdomains.

For automation, set `SLOPBOX_TOKEN` and pass it as `?t=` or `X-Slopbox-Token`.
For trusted local testing, `--no-auth` / `SLOPBOX_NO_AUTH=1` disables auth
entirely (this is what the Playwright suite uses).

## Remote access

To expose the box beyond localhost, set env vars and a tunnel comes up automatically:

| Method | Env vars |
|---|---|
| **Cloudflare Tunnel** | `CLOUDFLARE_TUNNEL_TOKEN` |
| **gatecrash** ([jclement/gatecrash](https://github.com/jclement/gatecrash)) | `GATECRASH_SERVER`, `GATECRASH_TOKEN`, `GATECRASH_HOST_KEY` — or mount `/etc/gatecrash/client.toml` |
| **none** | local port only — bring your own SSH tunnel / Tailscale / proxy |

**Docker access:** add `-v /var/run/docker.sock:/var/run/docker.sock` (or
`./run.sh --docker`) to drive containers from a session. The image ships only the
docker *client* — no daemon, no docker-in-docker; the entrypoint grants the
`ubuntu` user access to the mounted socket.

---

## Persistence — one folder

Everything mutable lives under **`/home/ubuntu`**, so a single bind mount captures
all state:

```
/home/ubuntu/
├── worktrees/              git worktrees
├── linuxbrew/              Homebrew prefix (symlinked from /home/linuxbrew)
├── .local/share/mise/      mise-installed runtimes/tools
├── .local/share/slopbox/   postgres data + logs
├── .claude, .codex, …      agent logins
└── .zshrc, .zsh_history    shell config (seeded on first run)
```

The image itself is **immutable** and rebuilt by CI — pull a new one, restart,
your state is untouched. Bind-mount the whole folder, or just the parts you want.

---

## How it works

Architecture, the PTY/terminal model, activity detection, and the security model
are documented in **[DESIGN.md](DESIGN.md)**. Current build status and known gaps
are in **[STATUS.md](STATUS.md)**. The product brief is **[idea.md](idea.md)**.

**Build & release:** `Dockerfile` is multi-arch; `.github/workflows/build.yml`
builds `linux/amd64 + linux/arm64` and pushes to `ghcr.io/jclement/slopbox`
(`:edge` on main, `:vX.Y.Z`/`:latest` on tags, plus a weekly tooling refresh).
