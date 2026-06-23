# Device link (split host-presence from the backend)

Status: design, in progress. Owner: Jeff.

## Why

Today the coordinator is two things fused into one host process: a **control
plane** (Docker lifecycle, registry, web UI, event watching, auth, the worker
reverse proxy) and a **host-presence layer** (open browser, bind host ports,
relay the SSH agent, soon clipboard + `op`). They're one process only because you
run `sr` on the same machine you sit at.

Almost everything in the host-presence layer should follow the *human*, not the
Docker host. When the backend runs on a heavy Linux box in the back room:

- `localhost:5173` must resolve on **your laptop**, so the port-forward listener
  has to terminate on the device, not the backend.
- "Open URL" must launch **your** browser.
- The SSH agent / 1Password you care about live on **your laptop**, not the box.

So we split out a **device** — a thin peer that provides host presence — while the
backend keeps everything else. One binary, three modes:

- `sr`              — backend **and** an in-process local device (today's behavior, unchanged).
- `sr serve`        — backend only (e.g. the Linux box; UI already exposed on the tailnet).
- `sr connect <url>` — device only; dials a remote backend.

> Naming: it's a **device**, not an "agent". "Agent" is already taken three times
> here — the AI agents (Claude/Codex) run *inside workers*, plus the ssh-**agent**.

## The load-bearing insight: the device link is an SSH connection, device-initiated

Port mapping today (`cmd/sr/portmap.go`) binds `net.Listen("tcp","127.0.0.1:<hostPort>")`
*in the coordinator process* and bridges each accepted conn to `cl.Dial(worker, containerPort)`
over the worker's sshd tunnel. The worker side never changes (Docker is on the
backend; coordinator↔worker stays loopback SSH). The **only** thing that relocates
to the device is that host listener.

**The device link is an SSH connection where the device is the client** (it dials
out — NAT/Tailscale friendly) and the coordinator is the server. We already depend
on `golang.org/x/crypto/ssh`, but only as a *client* (dialing workers); slice 2 is
a genuine net-new **SSH server** build (see slices below).

> ⚠️ Not `-R`/`tcpip-forward`. It's tempting to call `bind_port` a standard SSH
> remote-forward, but `tcpip-forward` opens the listener on the **server** — here
> the coordinator, the exact opposite of "localhost:5173 resolves on the laptop".
> Because the device is the client, **every** capability is **device-initiated**:
> the device opens channels *to* the coordinator. That's the same shape as the
> agent/op relays, so there's one uniform model.

**`bind_port`, concretely:** the *device* runs `net.Listen("127.0.0.1:5173")`
locally and, on each accept, opens a custom SSH channel (`forward-port@sr`,
carrying the target worker+port) to the coordinator. The coordinator's channel
handler runs the **inner bridge body** of `serve()` — `cl.Dial(worker, containerPort)`
+ bidirectional `io.Copy` — *not* its listener. So we reuse the bridge, relocate
the listener. The coordinator demuxes inbound channels back to `(worker, containerPort)`
via its existing `fwds`/`portStore` table, so worker identity rides the channel
open-request, and the coordinator still validates against `reservedPorts` (now in
the inverted direction).

## Capability taxonomy

Net-new device-link capabilities — all **device-initiated channels** (the device
opens them to the coordinator):

| Capability  | Mechanism                                                              | Reuses |
|-------------|------------------------------------------------------------------------|--------|
| `bind_port` | device listens locally, opens a `forward-port@sr` channel per accept   | `serve()` bridge body |
| `ssh_agent` | in-worker unix socket → channel to the device's agent socket           | `ForwardAgent` (portmap.go:214) |
| `commands`  | in-worker `cmd-relay.sock` → `cmd@sr` channel; device runs an exposed CLI tool | agent-relay shape + `internal/cmdrelay` |

`commands` is a **generic CLI passthrough**, not an op feature — `op` is just the
first tool. A device lists tools in `device.toml` (`commands = ["op", "gh", …]`);
each appears in containers under the same name and runs **on the device** with its
local auth (1Password unlock, cloud creds, browser SSO). The framing
(`internal/cmdrelay`) carries argv + curated env + stdin → stdout/stderr/exit and
rides any duplex stream (SSH channel, the in-worker unix socket, or an in-process
pipe for the local device). The device is authoritative: it runs only tools in its
exposed list.

**Open-URL and clipboard are NOT device-link capabilities.** They already ship via
the existing browser bridge: container `sr-open`/`sr-copy`/`open` shims POST to
`/api/bridge`, the worker fans out over SSE, and the **browser** does `window.open`
/ `navigator.clipboard.writeText` ([bridge.go](../internal/server/bridge.go),
[app.js](../web/app.js) `handleBridge`). In the remote workflow the browser is
already on the laptop (you load the tailnet UI there), so open/copy already land on
the right machine — and `handleBridge`'s localhost-open path composes with a
device-side `bind_port`. Don't build a second path. The only net-new clipboard
direction is **device→container read-clipboard**, which the push-only SSE bridge
can't do; it's deferred and needs a browser user-gesture.

### Forwarded commands, concretely (`op`, `gh`, …)

Goal: a CLI inside a container transparently drives the **device's** copy of that
tool (1Password biometric unlock, cloud creds, browser SSO — none of which exist
in the container).

Worker side reuses the SSH-agent-relay shape: the coordinator opens a unix listener
inside the worker (`cmdRelaySock`, like `agentRelaySock`) and a generic shim
(`shellraiser cmd-shim <name>`, installed per exposed tool) connects to it, framing
`{name, argv, curated env, stdin}` ↔ `{stdout, stderr, exit}` (`internal/cmdrelay`).
The coordinator dumb-pipes that to a `cmd@sr` channel; the **device** runs the tool
if its `device.toml` exposes that name. `op` gets an extra subcommand policy.

> Security: forwarding argv to a real tool on the device is powerful, so:
> exposure is per-tool and opt-in; `op` rejects `run`/`inject`/`plugin`/`signin`/
> `account`/`vault`/… and any `--` separator (arbitrary-exec / vault-exfil); only an
> env **whitelist** is forwarded. Precedence vs `OP_SERVICE_ACCOUNT_TOKEN` ([env] in
> config.toml): the service-account token wins — the entrypoint installs the `op`
> shim only when no token is present.

**The `op run` / `op inject` nuance (resolve on device, exec/write in container).**
`op run -- cmd` and `op inject` are sugar for "resolve `op://` references, then run
a command / write a file" — and that command/file would otherwise execute on the
**device** (an RCE channel). So the `op` shim special-cases them: it asks the device
to resolve only the `op://` secrets (via `op read`, which the policy allows) and
then runs the command / writes the file **in the container**, where danger-mode
already lives. The device never execs anything but `op read`. Everything else
(`op item get`, `op document get`, …) forwards verbatim.

## Enrollment — interactive, approve-the-connecting-key

Every device authenticates with **its own keypair** (at `ssh/device_ed25519`),
checked against the backend's `authorized_devices` allowlist. Enrollment is just
how a pubkey lands there. We do **interactive approval** (pairing / device flow —
*not* OAuth; no third-party IdP, no redirect dance), and crucially the **approved
key must equal the connecting key by construction** — not merely linked by a nonce.

1. `sr connect https://backend` — the device loads/creates its key and **completes
   the SSH handshake first**, landing in a server-side `pending` state (the
   connection authenticated *that* pubkey, so possession is proven).
2. The device opens the browser to `https://backend/enroll?code=<nonce>` (the
   pubkey is **not** carried in the URL fragment). The web UI — already
   authenticated via the existing coordinator **password** — renders the approval
   form showing the **device-chosen name + the ed25519 fingerprint of the live
   pending connection**, keyed by a server-issued, single-use, short-TTL,
   constant-time-compared nonce.
3. On approve, the backend writes that exact pubkey to `authorized_devices` and
   signals the pending connection; the device writes
   `~/.config/shellraiser/device.toml` (name, backend URL + pinned host key,
   granted capabilities).
4. Later `sr connect` reconnects with the key against the pinned host key — no
   browser.

The coordinator password gate is the trust anchor: only the authenticated owner
approves. How the blocked `sr connect` learns it was approved (hold the pre-auth
connection / long-poll), and where pending state lives (TTL, survives a daemon
restart mid-pairing), are spelled out in slice 3.

### Server authentication is host-key pinning, not the network

The link's server auth is **SSH host-key pinning**, not "it's only Tailscale"
(`sr connect <url>` accepts any URL). The backend has a **dedicated, persisted**
host key `ssh/backend_host_ed25519`, generated once by `sr serve`, stable across
restarts, **distinct from `coordinatorSigner`** (which is a client key for dialing
workers). Its fingerprint is returned in the `/enroll` response — so first-connect
is verified against the already-authenticated HTTPS session, **not blind TOFU** —
and pinned in `device.toml`'s `host_key`. A later mismatch aborts hard; legitimate
rotation needs a documented re-pair/reset path.

### The device owns what it will run — at capability granularity

`device.toml` is the **device-side source of truth**, enforced at runtime: the
backend can only *request* a capability; the device honors it solely if its config
grants it. But be precise about the boundary — enforcement controls the **menu, not
the meal**. Enrolling a backend grants it (and transitively the danger-mode worker
agents it fronts) the listed host capabilities, and *within* an enabled capability
the backend has broad latitude:

- `ssh_agent` — a live **signing oracle** on your real keys (same risk class as
  today's default-off `ssh_passthrough`).
- `op` — **arbitrary local exec** unless subcommand-allowlisted (see above).
- `bind_port` — arbitrary backend-controlled bytes into a localhost listener you
  trust as "my dev server".

So the default `device.toml` is a **minimal set**; `ssh_agent` and `op` are
explicit high-trust opt-ins (ideally with on-device confirmation). See the README's
"the container is the blast radius" model — enrolling a remote backend deliberately
extends that blast radius onto the device for the granted capabilities.

```toml
# ~/.config/shellraiser/device.toml   (matches cmd/sr/devicecfg.go)
name = "jeff-mac"

[[backend]]                              # array-of-tables, one per enrolled backend
url      = "https://shellraiser.tailXXXX.ts.net"
host_key = "SHA256:…"                    # backend host key, pinned at enrollment
capabilities = ["bind_port"]             # device-authored, device-enforced; minimal by default
# add "ssh_agent" only as a deliberate high-trust opt-in
commands = ["op"]                        # CLI tools workers may run on this device (op, gh, …)
# this device's own identity key lives at ssh/device_ed25519 (not inline here)
```

## Reconnects & error handling

- **Device dial loop**: exponential backoff + jitter; retains its intended binds
  and re-issues them on every reconnect.
- **Liveness**: SSH needs **app-level keepalive** — periodic `keepalive@sr` global
  requests (~50s) plus a read deadline on the `ssh.Conn`. (Not the terminal WS
  ping/pong — that's a different protocol.) `Map()` must not early-return a cached
  `hostPort` whose device channel is already dead.
- **Backend**: a dropped device's forwards are marked dead-but-remembered (the
  `portStore` already remembers host ports across restarts) and re-armed on return.
- **Structured results**: every command returns `{ok}` or `{error: "..."}` so the
  UI shows a real message ("port 5173 already bound on device 'jeff-mac'",
  "device offline", "op not granted on this device") instead of hanging.

## Two workflows

- **Local (backend on the Mac)** — `sr`, unchanged. The in-process local device runs
  directly (no transport); bind/agent/op all hit the Mac.
- **Remote (backend on the Linux box, via Tailscale)** — `sr serve` on the box
  (UI on tailnet:443, `cmd/sr/tailnet.go`); `sr connect <tailnet-host>` on the Mac,
  approve once. You browse the tailnet HTTPS UI, but device integration is local.

## Projects & registration (the device link is NOT a registration channel)

Multi-project is orthogonal to the split and **unchanged**. A worker is a
container, so projects live where Docker + the repo + the coordinator are: the
**backend**. Registering a project stays backend-local over the existing unix
socket (`sr.sock`). Today's `sr` does three separable jobs; the split pulls them
apart without changing registration:

| Job | Today | After split |
|-----|-------|-------------|
| ensure the coordinator | first `sr` auto-spawns | `sr serve` (explicit/headless) or auto |
| register cwd as a project | `sr` → local unix socket | **unchanged, backend-local** |
| open browser / presence | this host | routed to the active **device** |

- **On the backend box:** `sr serve`, then `cd p1 && sr`, `cd p2 && sr` register
  projects into the one coordinator — exactly as today.
- **On a laptop, backend remote:** `sr connect` attaches host-presence **only**; it
  does not register projects (the repo/Docker aren't there). Project management
  stays on the backend.

Payoff: running `cd p1 && sr` on the headless box while sitting at the laptop
routes "open browser" to the laptop (the active device), not the box.

**Decision — MVP registers on the backend; remote registration is deferred.**
Letting the laptop spin up a project on the remote backend requires the repo to
already exist on the backend's filesystem and grows the control plane from a local
unix socket into an authenticated network endpoint. Keeping it out preserves the
device link as a pure host-presence channel (smaller attack surface).

## Deferred

- **Multi-device active routing** — open-URL/copy stay on the existing browser
  bridge (last-connected UI wins). `bind_port` fan-out is **not** free policy: the
  `portStore`/`PortMapper`/ports-API/UI are single-host-valued today and must become
  **per-device** before two devices can bind simultaneously. Until that lands,
  enroll only one device.
- **Device→container read-clipboard** — the only net-new clipboard direction; can't
  reuse the push-only SSE bridge and needs a browser user-gesture.
- **Revocation & key hygiene** — `authorized_devices` needs a per-device UI revoke +
  `added`/`last_used` timestamps (interim: hand-edit `config.toml`). The device key
  is an **unencrypted at-rest credential** — keep `ssh/` out of synced/cloud dirs;
  OS-keychain/passphrase is a deferred hardening.
- **Join tokens** (headless self-enroll) — same allowlist mechanism, auto-approved.

## Decisions (resolved in review)

1. **Any network is fine.** Security rests on **mutual key auth + the pinned backend
   host key**, not network reachability — so `sr connect <url>` works over any
   network, not just the tailnet. The SSH listener address is therefore
   *configurable* (not tailnet-gated): default to the coordinator's configured
   interface. Because the listener may be publicly reachable, fail-closed key auth,
   per-key rate-limiting/lockout, and channel caps are **mandatory**, not optional.
2. **`op` precedence** — a worker's `OP_SERVICE_ACCOUNT_TOKEN` in `[env]` wins; the
   `op` shim is installed only when no service-account token is present.
3. **The device is the sole authority; the backend never enforces capabilities.**
   The backend may *request* anything; the device **silently ignores** any capability
   it hasn't granted in `device.toml`. So `authorized_devices` stores only the pubkey
   allowlist (name/key/added) — no capability mirror — and the enrollment form's
   capability checkboxes configure the **device** (written to `device.toml`), not the
   backend. No backend-side capability state to keep in sync.
4. **Backend tailnet binds and device binds coexist (additive).** The existing
   `bindTailnet` (worker port on the backend's tailnet IP) is **unchanged**; a
   device-side `bind_port` is *additional*. A worker port can be reachable via the
   backend tailnet IP **and/or** one or more device loopbacks at once — they're
   different hosts/IPs, no conflict. (Multiple *devices* still need per-device
   routing — see Deferred.)
5. **Long-poll enrollment.** The device completes the SSH handshake (proving key
   possession) into a server **`pending`** holding state, then **long-polls**
   `GET /enroll/status?code=<nonce>` over the authenticated HTTPS UI; the call blocks
   until the owner approves/denies or the TTL (~5 min) expires. On approval the
   server adds the key to `authorized_devices`, promotes the held SSH connection, and
   the long-poll returns the assigned name **+ the authoritative host-key
   fingerprint**. The device verifies the host key it saw on the SSH handshake against
   that HTTPS-delivered fingerprint (closing the first-connect TOFU gap with the
   already-authenticated channel), pins it, and writes `device.toml`. Pending state is
   in-memory with a TTL and does **not** survive a daemon restart mid-pairing (just
   re-run `sr connect`).

## Build slices

1. ✅ **Device interface.** Host-presence (`Forward`/`DialAgent`/`DialCmd`/`OpenURL`/
   `Grants`) behind a `Device` interface; default `sr` runs an in-process
   `localDevice`. No user-visible change.
2. ✅ **SSH server + `sr connect`/`sr serve`** ([devicelink.go](../cmd/sr/devicelink.go),
   [deviceclient.go](../cmd/sr/deviceclient.go)). Net-new SSH server: `ssh.ServerConfig`
   + `NewServerConn`, persisted `backend_host_ed25519`, `PublicKeyCallback` against
   `authorized_devices` (**fail closed**), per-key lockout ([authlockout.go](../cmd/sr/authlockout.go)).
   `bind_port` = device-initiated `forward-port@sr` channel → coordinator runs the
   `serve()` bridge body. App-level `keepalive@sr` + reconnect. Listener address
   configurable (`device_link_addr`). *Verified end-to-end in tests.*
3. ✅ **Interactive enrollment** ([enroll.go](../cmd/sr/enroll.go)). Possession-proven
   `/enroll/start`, owner approval form (name + fingerprint compare + capabilities +
   exposed commands), long-poll `/enroll/status` delivering host-key fp + endpoint,
   auto-written `device.toml`. *Verified in tests, incl. substitution-attack reject.*
4. ✅ **`ssh_agent` over the link.** `agent@sr` channel; agent sourced from the device.
5. ✅ **Forwarded commands** ([internal/cmdrelay](../internal/cmdrelay/cmdrelay.go) +
   [cmd/worker/shim.go](../cmd/worker/shim.go)). Generic CLI passthrough (`op`, `gh`, …)
   via `cmd-relay.sock` → `cmd@sr`; per-tool exposure; `op` subcommand policy + env
   whitelist; `op run`/`op inject` resolve-on-device/exec-in-container split.
   *Relay + policy verified in tests; container shim needs an image rebuild to verify
   end-to-end.*
6. **Deferred:** per-device routing (make `portStore`/ports-API per-device for
   multiple simultaneous devices), read-clipboard, revocation UI, join tokens.
