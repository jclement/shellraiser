# shellraiser — vision & roadmap

Synthesis of a ~30-agent review (architects, backend devs, UX/product, power-users,
safety, positioning) against a competitor scan (Superset, Conductor, Sculptor,
container-use, OpenHands, Terragon, Vibe Kanban, Crystal/Nimbalyst, Claude Squad,
Aider, Cursor/Devin). Source: workflow `wf_88fd156e-3ce`.

## North star

> shellraiser is the local-first, single-binary control plane where every parallel
> coding agent gets a **real isolated runtime** — its own container, network, volume,
> port-range, optional postgres — fanned out across a **glanceable triage board** and
> **reviewable from your phone** over **your own hardware**, with zero per-project
> config and zero metered cloud.

## Positioning

Every rival gives each agent a **folder** (a git worktree on your shared host) or
rents you a **metered cloud VM**. shellraiser gives each agent a **real machine on
your machine** — own net, volume, ports, db, idle-stopped to ~nothing — and lets you
drive the whole fleet from an iPad over your own Linux box with no ACU bill. **Not
another worktree manager: the only one where the blast radius is a container, not your
laptop.**

The competitive finding that anchors this: a survey of 9 OSS orchestrators found
**zero** use container isolation — all share one host's filesystem/db/ports. A whole
cottage industry (worktree-compose, hand-rolled port-math) exists to fake what
shellraiser gets for free. **Lead with runtime isolation, loudly.**

## Scorecard (8 pillars)

| Pillar | Grade | One-line |
|--------|-------|----------|
| 1. Manage many parallel threads | **C** | No fan-in: a finishing agent in project B is invisible while you view A. No triage board, no global ceiling — silently fails past ~10 threads. |
| 2. Great UX | **C+** | Solid PTY/xterm/palette/mobile chrome, but the named bottleneck — **diff review** — is punted to a terminal (git stats only). |
| 3. System integration | **B+** | SSH/git passthrough, agent + op/gh relays, code-server, /p proxy. Missing per-worktree port range, gitignored-file copy, open-in-host-editor. |
| 4. Isolation / safety | **A-** | The moat, and it's real: container/net/volume per project, caps, token-fenced API, no docker.sock. Docked for bare-metal escape hatches + invisible-in-UI. |
| 5. Single-machine story | **A-** | Docker-as-truth + reconcile + lazy-resume + idle-stop is best-in-class. Held back by per-poll docker-exec storm + no admission control. |
| 6. Remote-dev story | **B** | The device-link split-presence is the best design in the field — but it's a docs footnote, lacks per-device routing, push, and a powered-off-backend read deadline. |
| 7. Energy-efficient | **B+** | Idle auto-stop + busy-aware reaping + content-hashed overlays is genuinely efficient. Loses points for cold first-build + the per-poll inspect storm. |
| 8. Resilient | **B-** | Reconcile + reconnect are excellent. Holes: no `--restart` policy, unsupervised coordinator, **zero session durability** (idle-reap/OOM vaporizes a 40-min run). |

## Quick wins (stabilize the spine)

1. **Per-worktree PORT RANGE via env** (steal Conductor's `CONDUCTOR_PORT..+9`). Inject
   `SR_PORT`/`SR_PORT_1..9` alongside `SHELLRAISER_ID/NAME` so N dev servers never
   collide on :3000. Plumbing already in `worker.go:280-322`. **[S]**
2. **`--restart unless-stopped` + operator-stopped label.** `worker.go:267` runs with no
   restart policy → reboot/colima-restart leaves every worktree dark. One flag + a label
   to distinguish idle-reaped vs crashed = self-healing fleet. **[S]**
3. **Files-to-copy list in `.shellraiser.toml`** for gitignored files (`.env.local`).
   Worktrees live outside `/work` → fresh worktree silently misses `.env`. Config already
   merges arrays. **[S]**
4. **Fix `reconcile()` from O(1+5N) execs to one `docker ps --format` + cache.** Today
   ~151 docker fork/execs for 30 workers on *every* UI poll from *every* tab, synchronously
   inside `handleAPIWorkers` + `handleStats`. The dominant scaling wall. **[M]**
5. **Key device presence on authenticated pubkey, not the hello name; add a device-side
   keepalive read deadline.** Two devices with the same hostname stomp each other
   (`devicelink.go:189`); a powered-off backend hangs the laptop's listeners for minutes
   (`deviceclient.go:265`). The two worst remote bugs, both surgical. **[S]**
6. **Clear the port store on nuke + content-address `boxID`** (basename + short path hash).
   `nuke` never calls `ports.del` (`coordinator.go:290`) → stale ports leak; `boxID` falls
   back to bare basename (`worker.go:182`) so two repos named `api` collide across registry,
   proxy cache, networks, and `/w/<id>/`. **[S]**

## Big bets

1. **First-class diff/review surface** — inline syntax-highlighted per-worktree diff, an
   explicit "ready for review" state (keyed off the existing ding/activity machine),
   commit-per-step (Aider pattern), one-click commit/PR. *Review is the product at 10
   agents (~10 diffs/hour); today it's punted to a terminal.* **[L]**
2. **Coordinator-level event aggregator + triage board** — one SSE stream multiplexing
   every worker's `/api/events`, then a top-level board (running / needs-input / ready /
   done). The fan-out half works; the **fan-in half is missing**. Pillar 1 lives here. **[L]**
3. **Real push + phone-first approve/nudge PWA** — distinguish done vs blocked vs
   permission-prompt, deliver web-push over the device-link with a deep link, approve
   permissions from the phone. Match Nimbalyst's iOS bar with a PWA, not a native app. **[L]**
4. **Make the device-link a marquee onboarding path** — promote `sr connect <linux-box>`
   to a first-run path, finish per-device routing (multi-host portStore keyed on pubkey),
   lead marketing with self-hosted-remote + isolation. The durable two-pillar moat. **[L]**
5. **Session durability** — tmux-backed PTYs (already on the DESIGN roadmap) for restart
   survival "for free" + a per-environment command journal (container-use's "see what
   agents actually did"). Turns "trustworthy" from claim to guarantee. **[L]**
6. **Pre-bake + warm-pool per-project images** — Sculptor's 10x: deps in the base layer,
   install-before-copy-code, warm pool, auto-detect-and-build a Dockerfile. First agent
   starts in seconds. **[M]**

## Borrow

- **Conductor:** per-workspace port range; setup/run/archive script triad with
  `run_mode=concurrent|nonconcurrent`; "Files to copy"; notify-when-an-agent-**needs-input**.
- **Superset:** glanceable status lanes as the triage board; one-click open-in-editor.
- **Sculptor:** deps-in-base-layer caching + warm pool; auto-generate a Dockerfile.
- **Nimbalyst:** the remote bar (deep-linked push + approve-from-phone + diff-before-save) —
  via a PWA, not native.
- **Aider/container-use:** commit-per-step history; plain-git review; command-history
  capture; first-class "take over this agent".
- **OpenHands/Terragon/Sweep:** an inbound **trigger** surface (GitHub/Slack → worktree+agent).

## Cut / avoid

- **No cloud-only / metered hosting.** Terragon *and* Vibe Kanban (cloud/standalone) shut
  down in 2026; the survivors are local-first OSS. Single-machine-first + optional
  self-hosted-remote is the durable lane.
- **No native mobile app.** A responsive web UI + `/p` proxy + a PWA hits the same three
  remote actions at a fraction of the ship/update cost.
- **Don't compete on agent breadth.** "22 providers" commoditizes instantly — it's hygiene,
  not a moat. Spend the budget on the layer *below* the agent.
- **Stop letting bare-metal mode metastasize** (~8 tier-crossing branches that defeat the
  isolation story). Model it behind the Device-style seam or quarantine it.
- **No heavy kanban-as-orchestrator.** Lightweight status lanes over the existing activity
  machine give 90% of the value at 10% of the build.
- **Don't monetize the core.** Keep the single-binary core free-feeling; if anything is paid,
  it's the remote/fleet/team-governance layer.

## Roadmap (ordered)

- **M0 — Stabilize the spine (1-2 wks):** reconcile→single `docker ps` + cache; content-address
  `boxID` + clear port store on nuke; `--restart unless-stopped`; device presence keyed on
  pubkey + device-side keepalive deadline. *Remove the scaling wall + the worst remote bugs
  before adding surface.*
- **M1 — Many-thread fan-in (2-3 wks):** coordinator SSE aggregator + status-lane triage board.
  *Pillar 1; the reason to open the app.*
- **M2 — Review is the product (3-4 wks):** inline diff, ready-for-review state, commit-per-step,
  one-click commit/PR.
- **M3 — Isolation ergonomics (2 wks):** per-worktree port range, files-to-copy, setup/run/archive
  hooks with `run_mode`, make isolation legible in-UI.
- **M4 — Remote & phone as marquee (3-4 wks):** promote `sr connect`, finish per-device routing,
  ship web-push + a phone PWA (approve / nudge / diff).
- **M5 — Resilience & speed (2-3 wks):** tmux-backed durable sessions + command journal; supervised
  coordinator (`sr service install` + panic-recovery + admission ceiling); pre-bake/warm-pool images.
- **M6 — Triggers & take-over (1-2 wks, opportunistic):** inbound GitHub/Slack webhook; "take over
  this agent" + "open in host editor" over the device-link relay.

## Top risks

1. **Differentiation drift** — pouring effort into agent-breadth + a prettier shell
   (commoditized) instead of isolation/remote/review/notifications. *Rule: every sprint
   advances a moat pillar.*
2. **Review surface is a deceptively large build** — ship read-only inline diff + ready-state
   first; defer comment-to-fix.
3. **Unsupervised coordinator + zero session durability** — an unattended overnight fan-out
   can lose hours on an OOM/idle-reap race. Consider pulling tmux-backing forward.
4. **Bare-metal mode + singleton-device state** are latent incoherences (a global `hostCfg`
   read/written by all three tiers is a live data race). Pay down before M4 multi-device.
5. **Self-hosted-remote onboarding friction** — if `sr connect` isn't genuinely
   2-minutes-to-working, the marquee differentiator stays a docs footnote.
