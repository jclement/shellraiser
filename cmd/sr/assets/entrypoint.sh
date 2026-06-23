#!/usr/bin/env bash
# shellraiser entrypoint: seed persistent home, start postgres + pgweb, optionally
# open an external tunnel, then drop to 'ubuntu' and run the web app.
#
# Everything mutable lives under /home/ubuntu so a single bind mount captures
# all state: worktrees, mise installs, the brew prefix (symlinked), postgres
# data, agent logins, shell history.
set -euo pipefail

USERNAME=ubuntu
HOME_DIR=/home/ubuntu
PORT="${SHELLRAISER_ADDR:-}"; PORT="${PORT##*:}"; PORT="${PORT:-7000}"
# Run a command as 'ubuntu' with HOME pointed at the persistent mount (so agent
# logins, mise installs, lazy-downloaded tools, etc. land in /home/ubuntu).
run_user() {
  gosu "$USERNAME" env HOME="$HOME_DIR" \
    PATH="$HOME_DIR/.local/bin:$HOME_DIR/.local/share/mise/shims:/home/linuxbrew/.linuxbrew/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin" \
    "$@"
}

# Lazy installers — fetch optional tools into the persistent home on first use
# so the base image stays lean and only pulls what you actually enable.
ensure_code_server() {
  [ -x "$HOME_DIR/.local/bin/code-server" ] && return 0
  # Remove any stale/broken install (e.g. a root-owned one from an older image)
  # AS ROOT so the fresh ubuntu-user install can't be blocked by it.
  rm -rf "$HOME_DIR"/.local/lib/code-server-* "$HOME_DIR"/.local/bin/code-server \
         "$HOME_DIR"/.cache/code-server 2>/dev/null || true
  echo "shellraiser: installing code-server into the home volume (first run)…"
  run_user sh -c 'curl -fsSL https://code-server.dev/install.sh | sh -s -- --method standalone --prefix "$HOME/.local"'
}

# 1. Seed the (possibly empty / foreign-owned) persistent home mount. Make the
#    home root + the standard XDG dirs ubuntu-owned so the ubuntu user can write
#    there (mise, code-server, etc. install into them). We chown those dirs
#    NON-recursively so we never recurse into the symlink-laden code-server tree
#    (~/.local/lib) or the large mise installs — under `set -e` that chown's
#    non-zero exit would otherwise abort the whole entrypoint.
chown "$USERNAME:$USERNAME" "$HOME_DIR" 2>/dev/null || true
mkdir -p "$HOME_DIR/.local/bin" "$HOME_DIR/.local/share/shellraiser" "$HOME_DIR/.local/state" \
         "$HOME_DIR/.config/shellraiser" "$HOME_DIR/.cache" "$HOME_DIR/worktrees" "$HOME_DIR/linuxbrew"
chown "$USERNAME:$USERNAME" \
  "$HOME_DIR/.local" "$HOME_DIR/.local/share" "$HOME_DIR/.local/state" \
  "$HOME_DIR/.config" "$HOME_DIR/.config/shellraiser" "$HOME_DIR/.cache" "$HOME_DIR/linuxbrew" 2>/dev/null || true
chown -R "$USERNAME:$USERNAME" \
  "$HOME_DIR/.local/bin" "$HOME_DIR/.local/share/shellraiser" "$HOME_DIR/worktrees" 2>/dev/null || true

if [ ! -f "$HOME_DIR/.zshrc" ] && [ -f /etc/skel/.zshrc ]; then
  cp /etc/skel/.zshrc "$HOME_DIR/.zshrc"
  chown "$USERNAME:$USERNAME" "$HOME_DIR/.zshrc" 2>/dev/null || true
fi

# Keep Homebrew's data under the home mount while preserving the standard prefix
# path (so prebuilt bottles still apply): symlink /home/linuxbrew → home mount.
if [ ! -L /home/linuxbrew ]; then
  rm -rf /home/linuxbrew
  ln -s "$HOME_DIR/linuxbrew" /home/linuxbrew
fi

# Install Homebrew into that (persistent, default-prefix) location on first boot,
# in the background so it never blocks startup. Because the prefix resolves to the
# standard /home/linuxbrew/.linuxbrew, prebuilt bottles apply and `brew install`
# is fast; everything lands in the home volume so it survives container recreates.
ensure_brew() {
  local PREFIX="/home/linuxbrew/.linuxbrew"
  [ -x "$PREFIX/bin/brew" ] && return 0
  echo "shellraiser: installing Homebrew in the background (first run)…"
  (
    run_user git clone --depth=1 https://github.com/Homebrew/brew "$PREFIX/Homebrew" >/dev/null 2>&1 \
      && run_user mkdir -p "$PREFIX/bin" \
      && run_user ln -sf ../Homebrew/bin/brew "$PREFIX/bin/brew" \
      && run_user bash -lc 'brew update --quiet' >/dev/null 2>&1 \
      && echo "shellraiser: Homebrew ready — try 'brew install <pkg>'" \
      || echo "shellraiser: ⚠ Homebrew install failed (run it again later)"
  ) &
}
ensure_brew

# 2. Postgres + pgweb (default on; SHELLRAISER_POSTGRES=0 to disable). Fully
#    non-fatal: if the data dir can't be secured (e.g. a Docker Desktop bind
#    mount, where chmod 0700 fails) or init fails, the box still boots with the
#    DB disabled and a clear hint to mount a named volume there.
start_postgres() {
  local PGBIN
  PGBIN="$(ls -d /usr/lib/postgresql/*/bin 2>/dev/null | sort -V | tail -1)"
  [ -n "$PGBIN" ] || { echo "shellraiser: postgres not installed"; return 1; }
  mkdir -p "$PGDATA"
  chown -R "$USERNAME:$USERNAME" "$(dirname "$PGDATA")" 2>/dev/null || true
  if ! chmod 700 "$PGDATA" 2>/dev/null; then
    echo "shellraiser: ⚠ cannot secure $PGDATA (bind mount?) — mount a named volume"
    echo "shellraiser:   there for postgres. Disabling DB for now."
    return 1
  fi
  if [ ! -s "$PGDATA/PG_VERSION" ]; then
    echo "shellraiser: initializing postgres (postgres/postgres)"
    echo "postgres" > /tmp/pgpw
    if ! run_user "$PGBIN/initdb" -D "$PGDATA" -U postgres \
        --auth-local=trust --auth-host=md5 --pwfile=/tmp/pgpw >/dev/null 2>&1; then
      rm -f /tmp/pgpw; echo "shellraiser: ⚠ initdb failed — disabling DB"; return 1
    fi
    rm -f /tmp/pgpw
  fi
  run_user "$PGBIN/pg_ctl" -D "$PGDATA" -l "$PGDATA/server.log" \
    -o "-h 127.0.0.1 -p 5432 -k /tmp" -w start >/dev/null 2>&1 \
    || { echo "shellraiser: ⚠ postgres failed to start — disabling DB"; return 1; }
  run_user pgweb --bind=127.0.0.1 --listen=8081 --prefix=db \
    --url "postgres://postgres:postgres@127.0.0.1:5432/postgres?sslmode=disable" \
    >"$HOME_DIR/.local/share/shellraiser/pgweb.log" 2>&1 &
  echo "shellraiser: postgres + pgweb (/db) ready"
  return 0
}

# v2 default: postgres OFF (opt-in per project via SHELLRAISER_POSTGRES=1 / config).
if [ "${SHELLRAISER_POSTGRES:-0}" != "0" ]; then
  # On failure, force the flag off so the web app hides the broken /db tab.
  start_postgres || export SHELLRAISER_POSTGRES=0
fi

# 2b. Install project-declared tools on startup (best-effort, non-fatal).
if [ -f /work/mise.toml ] || [ -f /work/.mise.toml ] || [ -f /work/.config/mise/config.toml ] || [ -f /work/.tool-versions ]; then
  echo "shellraiser: mise install (project tools)"
  run_user bash -lc 'cd /work && mise trust --yes . >/dev/null 2>&1; mise install -y' || echo "shellraiser: mise install had issues"
fi
if [ -f /work/Brewfile ] && [ -x /home/linuxbrew/.linuxbrew/bin/brew ]; then
  echo "shellraiser: brew bundle (Brewfile)"
  run_user bash -lc 'cd /work && brew bundle --file=/work/Brewfile' || echo "shellraiser: brew bundle had issues"
fi

# 2c. code-server at /edit (default on; SHELLRAISER_CODE_SERVER=0 to disable). Run
#     the (slow, first-time) install + start in the background so the web UI
#     comes up immediately instead of blocking on the code-server download.
start_code_server() {
  ensure_code_server || { echo "shellraiser: ⚠ code-server install failed — /edit disabled"; return; }
  CS_DIR="$HOME_DIR/.local/share/code-server"
  # Create dirs + seed settings AS the ubuntu user so nothing is root-owned and
  # no chown of the (symlink-laden) code-server tree is needed.
  run_user mkdir -p "$CS_DIR/extensions" "$CS_DIR/User"
  if [ ! -f "$CS_DIR/User/settings.json" ]; then
    run_user sh -c 'cat > "$HOME/.local/share/code-server/User/settings.json"' <<'JSON'
{
  "workbench.startupEditor": "none",
  "telemetry.telemetryLevel": "off"
}
JSON
  fi
  echo "shellraiser: starting code-server at /edit"
  run_user code-server --bind-addr 127.0.0.1:8082 --auth none --disable-telemetry \
    --user-data-dir "$CS_DIR" --extensions-dir "$CS_DIR/extensions" \
    >"$HOME_DIR/.local/share/shellraiser/code-server.log" 2>&1 &
  # GitLens from Open VSX on first run (best-effort, in the background).
  if [ ! -e "$CS_DIR/extensions/.gitlens-done" ]; then
    ( run_user code-server --install-extension eamodio.gitlens \
        --extensions-dir "$CS_DIR/extensions" >/dev/null 2>&1 \
      && run_user touch "$CS_DIR/extensions/.gitlens-done" ) &
  fi
}
[ "${SHELLRAISER_CODE_SERVER:-1}" != "0" ] && start_code_server &

# 3. If the docker socket was passed through, let 'ubuntu' use it.
if [ -S /var/run/docker.sock ]; then
  sock_gid="$(stat -c '%g' /var/run/docker.sock)"
  if ! getent group "$sock_gid" >/dev/null 2>&1; then
    groupadd -g "$sock_gid" hostdocker || true
  fi
  usermod -aG "$sock_gid" "$USERNAME" || true
fi

# 3b. Optional SSH server (key-only) with TCP forwarding — gives a real terminal
#     from any SSH client AND port-forwarding to internal ports (ssh -L ...).
#     Enabled when SHELLRAISER_SSH=1 or a public key is provided.
start_sshd() {
  local SSHDIR="$HOME_DIR/.ssh" KEYS HK PORT_SSH
  SSHDIR="$HOME_DIR/.ssh"; KEYS="$SSHDIR/authorized_keys"; HK="$SSHDIR/host_keys"
  mkdir -p "$HK"; touch "$KEYS"
  if [ -n "${SHELLRAISER_SSH_PUBKEY:-}" ] && ! grep -qxF "$SHELLRAISER_SSH_PUBKEY" "$KEYS" 2>/dev/null; then
    echo "$SHELLRAISER_SSH_PUBKEY" >> "$KEYS"
  fi
  # sshd starts even with no pre-shared key — the web UI's "Copy SSH" button mints
  # short-lived ephemeral keys on demand (key-only auth, so an empty file is safe).
  chown "$USERNAME:$USERNAME" "$SSHDIR" "$KEYS"; chmod 700 "$SSHDIR"; chmod 600 "$KEYS"
  [ -f "$HK/ssh_host_ed25519_key" ] || ssh-keygen -q -t ed25519 -f "$HK/ssh_host_ed25519_key" -N ""
  chmod 600 "$HK"/* 2>/dev/null || true
  PORT_SSH="${SHELLRAISER_SSH_PORT:-22}"
  mkdir -p /run/sshd
  cat > /etc/ssh/sshd_config.d/shellraiser.conf <<CONF
Port $PORT_SSH
AllowUsers $USERNAME
PubkeyAuthentication yes
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin no
# The coordinator (the only authorized key) opens -L tunnels to in-container
# loopback services and ONE remote unix forward (the host SSH-agent relay
# socket). AllowTcpForwarding must be `all` because OpenSSH gates the remote
# *direction* — including the streamlocal/unix relay — on it; we then re-narrow:
#   PermitOpen      → -L may only reach in-container loopback (port mapping)
#   PermitListen none→ no TCP -R (can't expose a worker port outward)
#   AllowStreamLocalForwarding remote → only the agent-relay unix -R is allowed
AllowTcpForwarding all
PermitOpen 127.0.0.1:* [::1]:*
PermitListen none
AllowStreamLocalForwarding remote
StreamLocalBindUnlink yes
GatewayPorts no
AllowAgentForwarding no
PermitTunnel no
X11Forwarding no
HostKey $HK/ssh_host_ed25519_key
AuthorizedKeysFile $KEYS
PidFile /run/sshd/sshd.pid
CONF
  echo "shellraiser: sshd on :$PORT_SSH (key auth, TCP forwarding) — ssh $USERNAME@<host> -p <published>"
  /usr/sbin/sshd -E "$HOME_DIR/.local/share/shellraiser/sshd.log" || echo "shellraiser: ⚠ sshd failed to start"
}
if [ "${SHELLRAISER_SSH:-0}" = "1" ] || [ -n "${SHELLRAISER_SSH_PUBKEY:-}" ]; then
  start_sshd
fi

# 4. Tailscale (optional, private mesh) — gives the box its own tailnet IP +
#    MagicDNS name (the SHELLRAISER_ID), reachable from your devices with every port
#    available, no public exposure. Userspace networking → no NET_ADMIN/tun
#    needed (works on Docker Desktop). State persists in the home volume.
if [ -n "${TAILSCALE_KEY:-}" ] || [ "${SHELLRAISER_TAILSCALE:-0}" = "1" ]; then
  if command -v tailscaled >/dev/null 2>&1; then
    TS_STATE="$HOME_DIR/.local/share/shellraiser/tailscale"
    mkdir -p "$TS_STATE" /var/run/tailscale
    tailscaled --tun=userspace-networking --state="$TS_STATE/state" \
      --socket=/var/run/tailscale/tailscaled.sock \
      >"$HOME_DIR/.local/share/shellraiser/tailscaled.log" 2>&1 &
    sleep 1
    if [ -n "${TAILSCALE_KEY:-}" ]; then
      tailscale up --authkey="$TAILSCALE_KEY" --hostname="${SHELLRAISER_ID:-shellraiser}" --accept-routes >/dev/null 2>&1 \
        && echo "shellraiser: tailscale up as ${SHELLRAISER_ID:-shellraiser} (tailnet)" \
        || echo "shellraiser: ⚠ tailscale up failed"
    else
      echo "shellraiser: tailscaled started — run 'sudo tailscale up --hostname=${SHELLRAISER_ID:-shellraiser}' to log in"
    fi
  else
    echo "shellraiser: ⚠ tailscale not installed"
  fi
fi

# 4b. Seed shared agent credentials. The coordinator mounts the shared creds
#     volume read-only at /agents (unless the project is isolated) and points
#     CLAUDE_CONFIG_DIR / CODEX_HOME at per-worker dirs in the home volume. Copy
#     ONLY the credential files in (never the hot .claude.json / sessions), and
#     never clobber a locally-refreshed token.
seed_agents() {
  [ -d /agents ] || return 0
  if [ -n "${CLAUDE_CONFIG_DIR:-}" ]; then
    run_user mkdir -p "$CLAUDE_CONFIG_DIR"
    if [ -f /agents/claude/.credentials.json ] && [ ! -f "$CLAUDE_CONFIG_DIR/.credentials.json" ]; then
      cp /agents/claude/.credentials.json "$CLAUDE_CONFIG_DIR/.credentials.json" 2>/dev/null || true
    fi
  fi
  if [ -n "${CODEX_HOME:-}" ]; then
    run_user mkdir -p "$CODEX_HOME"
    if [ -f /agents/codex/auth.json ] && [ ! -f "$CODEX_HOME/auth.json" ]; then
      cp /agents/codex/auth.json "$CODEX_HOME/auth.json" 2>/dev/null || true
    fi
  fi
  chown -R "$USERNAME:$USERNAME" "${CLAUDE_CONFIG_DIR:-/dev/null}" "${CODEX_HOME:-/dev/null}" 2>/dev/null || true
  echo "shellraiser: seeded shared agent credentials"
}
seed_agents

# 4c. SSH/git passthrough. The coordinator binds the host ~/.ssh read-only at
#     /ssh-host and forwards the host SSH agent at /ssh-agent (SSH_AUTH_SOCK).
#     Copy config + known_hosts into the user's ~/.ssh (never authorized_keys,
#     which sshd owns) and make the forwarded agent socket usable by uid 1000.
seed_ssh() {
  if [ -d /ssh-host ]; then
    SSHDIR="$HOME_DIR/.ssh"
    mkdir -p "$SSHDIR"
    for f in config known_hosts; do
      [ -f "/ssh-host/$f" ] && cp "/ssh-host/$f" "$SSHDIR/$f" 2>/dev/null || true
    done
    chown -R "$USERNAME:$USERNAME" "$SSHDIR" 2>/dev/null || true
    chmod 700 "$SSHDIR" 2>/dev/null || true
    echo "shellraiser: bound host ~/.ssh (config + known_hosts)"
  fi
  if [ -n "${SSH_AUTH_SOCK:-}" ] && [ -S "${SSH_AUTH_SOCK}" ]; then
    chmod 666 "$SSH_AUTH_SOCK" 2>/dev/null || true
    echo "shellraiser: SSH agent forwarded ($SSH_AUTH_SOCK)"
  fi
}
seed_ssh

# 4d. In-container bridge helpers: `open <url>` and `sr-copy` ask the web UI to
#     open a URL / copy text on YOUR machine — so it works over the tailnet too
#     (the browser does the work, not the container). They post to the worker API
#     with the injected worker token.
cat > /usr/local/bin/sr-open <<'SCRIPT'
#!/bin/sh
[ -z "$1" ] && { echo "usage: open <url|path>"; exit 1; }
curl -fsS -X POST -H "X-Shellraiser-Worker: ${SHELLRAISER_WORKER_TOKEN}" -H 'content-type: application/json' \
  --data "$(python3 -c 'import json,sys; print(json.dumps({"action":"open","url":sys.argv[1]}))' "$1")" \
  http://127.0.0.1:7000/api/bridge >/dev/null 2>&1 \
  && echo "→ opening in your browser: $1" || { echo "open: not connected to shellraiser"; exit 1; }
SCRIPT
cat > /usr/local/bin/sr-copy <<'SCRIPT'
#!/bin/sh
if [ "$#" -gt 0 ]; then data="$*"; else data="$(cat)"; fi
printf '%s' "$data" | python3 -c 'import json,sys; print(json.dumps({"action":"copy","text":sys.stdin.read()}))' \
  | curl -fsS -X POST -H "X-Shellraiser-Worker: ${SHELLRAISER_WORKER_TOKEN}" -H 'content-type: application/json' --data @- \
    http://127.0.0.1:7000/api/bridge >/dev/null 2>&1 \
  && echo "→ copied to your clipboard" || { echo "sr-copy: not connected"; exit 1; }
SCRIPT
chmod +x /usr/local/bin/sr-open /usr/local/bin/sr-copy
ln -sf /usr/local/bin/sr-open /usr/local/bin/open
ln -sf /usr/local/bin/sr-open /usr/local/bin/xdg-open

# 5. Run the app as the unprivileged user, with HOME + tool paths integrated so
#    directly-launched agents/editors see mise- and brew-installed tools.
export HOME="$HOME_DIR"
export PATH="$HOME_DIR/.local/share/mise/shims:/home/linuxbrew/.linuxbrew/bin:$HOME_DIR/.local/bin:/usr/local/bin:$PATH"
exec gosu "$USERNAME" shellraiser "$@"
