#!/usr/bin/env bash
# slopbox entrypoint: seed persistent home, start postgres + pgweb, optionally
# open an external tunnel, then drop to 'ubuntu' and run the web app.
#
# Everything mutable lives under /home/ubuntu so a single bind mount captures
# all state: worktrees, mise installs, the brew prefix (symlinked), postgres
# data, agent logins, shell history.
set -euo pipefail

USERNAME=ubuntu
HOME_DIR=/home/ubuntu
PORT="${SLOPBOX_ADDR:-}"; PORT="${PORT##*:}"; PORT="${PORT:-7000}"
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
  echo "slopbox: installing code-server into the home volume (first run)…"
  run_user sh -c 'curl -fsSL https://code-server.dev/install.sh | sh -s -- --method standalone --prefix "$HOME/.local"'
}

# 1. Seed the (possibly empty / foreign-owned) persistent home mount. Make the
#    home root + the standard XDG dirs ubuntu-owned so the ubuntu user can write
#    there (mise, code-server, etc. install into them). We chown those dirs
#    NON-recursively so we never recurse into the symlink-laden code-server tree
#    (~/.local/lib) or the large mise installs — under `set -e` that chown's
#    non-zero exit would otherwise abort the whole entrypoint.
chown "$USERNAME:$USERNAME" "$HOME_DIR" 2>/dev/null || true
mkdir -p "$HOME_DIR/.local/bin" "$HOME_DIR/.local/share/slopbox" "$HOME_DIR/.local/state" \
         "$HOME_DIR/.config" "$HOME_DIR/.cache" "$HOME_DIR/worktrees" "$HOME_DIR/linuxbrew"
chown "$USERNAME:$USERNAME" \
  "$HOME_DIR/.local" "$HOME_DIR/.local/share" "$HOME_DIR/.local/state" \
  "$HOME_DIR/.config" "$HOME_DIR/.cache" "$HOME_DIR/linuxbrew" 2>/dev/null || true
chown -R "$USERNAME:$USERNAME" \
  "$HOME_DIR/.local/bin" "$HOME_DIR/.local/share/slopbox" "$HOME_DIR/worktrees" 2>/dev/null || true

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

# 2. Postgres + pgweb (default on; SLOPBOX_POSTGRES=0 to disable). Fully
#    non-fatal: if the data dir can't be secured (e.g. a Docker Desktop bind
#    mount, where chmod 0700 fails) or init fails, the box still boots with the
#    DB disabled and a clear hint to mount a named volume there.
start_postgres() {
  local PGBIN
  PGBIN="$(ls -d /usr/lib/postgresql/*/bin 2>/dev/null | sort -V | tail -1)"
  [ -n "$PGBIN" ] || { echo "slopbox: postgres not installed"; return 1; }
  mkdir -p "$PGDATA"
  chown -R "$USERNAME:$USERNAME" "$(dirname "$PGDATA")" 2>/dev/null || true
  if ! chmod 700 "$PGDATA" 2>/dev/null; then
    echo "slopbox: ⚠ cannot secure $PGDATA (bind mount?) — mount a named volume"
    echo "slopbox:   there for postgres. Disabling DB for now."
    return 1
  fi
  if [ ! -s "$PGDATA/PG_VERSION" ]; then
    echo "slopbox: initializing postgres (postgres/postgres)"
    echo "postgres" > /tmp/pgpw
    if ! run_user "$PGBIN/initdb" -D "$PGDATA" -U postgres \
        --auth-local=trust --auth-host=md5 --pwfile=/tmp/pgpw >/dev/null 2>&1; then
      rm -f /tmp/pgpw; echo "slopbox: ⚠ initdb failed — disabling DB"; return 1
    fi
    rm -f /tmp/pgpw
  fi
  run_user "$PGBIN/pg_ctl" -D "$PGDATA" -l "$PGDATA/server.log" \
    -o "-h 127.0.0.1 -p 5432 -k /tmp" -w start >/dev/null 2>&1 \
    || { echo "slopbox: ⚠ postgres failed to start — disabling DB"; return 1; }
  run_user pgweb --bind=127.0.0.1 --listen=8081 --prefix=db \
    --url "postgres://postgres:postgres@127.0.0.1:5432/postgres?sslmode=disable" \
    >"$HOME_DIR/.local/share/slopbox/pgweb.log" 2>&1 &
  echo "slopbox: postgres + pgweb (/db) ready"
  return 0
}

# v2 default: postgres OFF (opt-in per project via SLOPBOX_POSTGRES=1 / config).
if [ "${SLOPBOX_POSTGRES:-0}" != "0" ]; then
  # On failure, force the flag off so the web app hides the broken /db tab.
  start_postgres || export SLOPBOX_POSTGRES=0
fi

# 2b. Install project-declared tools on startup (best-effort, non-fatal).
if [ -f /work/mise.toml ] || [ -f /work/.mise.toml ] || [ -f /work/.config/mise/config.toml ] || [ -f /work/.tool-versions ]; then
  echo "slopbox: mise install (project tools)"
  run_user bash -lc 'cd /work && mise trust --yes . >/dev/null 2>&1; mise install -y' || echo "slopbox: mise install had issues"
fi
if [ -f /work/Brewfile ] && [ -x /home/linuxbrew/.linuxbrew/bin/brew ]; then
  echo "slopbox: brew bundle (Brewfile)"
  run_user bash -lc 'cd /work && brew bundle --file=/work/Brewfile' || echo "slopbox: brew bundle had issues"
fi

# 2c. code-server at /edit (default on; SLOPBOX_CODE_SERVER=0 to disable). Run
#     the (slow, first-time) install + start in the background so the web UI
#     comes up immediately instead of blocking on the code-server download.
start_code_server() {
  ensure_code_server || { echo "slopbox: ⚠ code-server install failed — /edit disabled"; return; }
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
  echo "slopbox: starting code-server at /edit"
  run_user code-server --bind-addr 127.0.0.1:8082 --auth none --disable-telemetry \
    --user-data-dir "$CS_DIR" --extensions-dir "$CS_DIR/extensions" \
    >"$HOME_DIR/.local/share/slopbox/code-server.log" 2>&1 &
  # GitLens from Open VSX on first run (best-effort, in the background).
  if [ ! -e "$CS_DIR/extensions/.gitlens-done" ]; then
    ( run_user code-server --install-extension eamodio.gitlens \
        --extensions-dir "$CS_DIR/extensions" >/dev/null 2>&1 \
      && run_user touch "$CS_DIR/extensions/.gitlens-done" ) &
  fi
}
[ "${SLOPBOX_CODE_SERVER:-1}" != "0" ] && start_code_server &

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
#     Enabled when SLOPBOX_SSH=1 or a public key is provided.
start_sshd() {
  local SSHDIR="$HOME_DIR/.ssh" KEYS HK PORT_SSH
  SSHDIR="$HOME_DIR/.ssh"; KEYS="$SSHDIR/authorized_keys"; HK="$SSHDIR/host_keys"
  mkdir -p "$HK"; touch "$KEYS"
  if [ -n "${SLOPBOX_SSH_PUBKEY:-}" ] && ! grep -qxF "$SLOPBOX_SSH_PUBKEY" "$KEYS" 2>/dev/null; then
    echo "$SLOPBOX_SSH_PUBKEY" >> "$KEYS"
  fi
  # sshd starts even with no pre-shared key — the web UI's "Copy SSH" button mints
  # short-lived ephemeral keys on demand (key-only auth, so an empty file is safe).
  chown "$USERNAME:$USERNAME" "$SSHDIR" "$KEYS"; chmod 700 "$SSHDIR"; chmod 600 "$KEYS"
  [ -f "$HK/ssh_host_ed25519_key" ] || ssh-keygen -q -t ed25519 -f "$HK/ssh_host_ed25519_key" -N ""
  chmod 600 "$HK"/* 2>/dev/null || true
  PORT_SSH="${SLOPBOX_SSH_PORT:-22}"
  mkdir -p /run/sshd
  cat > /etc/ssh/sshd_config.d/slopbox.conf <<CONF
Port $PORT_SSH
AllowUsers $USERNAME
PubkeyAuthentication yes
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin no
# Coordinator opens only -L tunnels to in-container loopback services; lock the
# rest down so a compromised worker can't pivot outward through our SSH channel.
AllowTcpForwarding local
PermitOpen 127.0.0.1:* [::1]:*
GatewayPorts no
AllowAgentForwarding no
PermitTunnel no
X11Forwarding no
HostKey $HK/ssh_host_ed25519_key
AuthorizedKeysFile $KEYS
PidFile /run/sshd/sshd.pid
CONF
  echo "slopbox: sshd on :$PORT_SSH (key auth, TCP forwarding) — ssh $USERNAME@<host> -p <published>"
  /usr/sbin/sshd -E "$HOME_DIR/.local/share/slopbox/sshd.log" || echo "slopbox: ⚠ sshd failed to start"
}
if [ "${SLOPBOX_SSH:-0}" = "1" ] || [ -n "${SLOPBOX_SSH_PUBKEY:-}" ]; then
  start_sshd
fi

# 4. Tailscale (optional, private mesh) — gives the box its own tailnet IP +
#    MagicDNS name (the SLOP_ID), reachable from your devices with every port
#    available, no public exposure. Userspace networking → no NET_ADMIN/tun
#    needed (works on Docker Desktop). State persists in the home volume.
if [ -n "${TAILSCALE_KEY:-}" ] || [ "${SLOPBOX_TAILSCALE:-0}" = "1" ]; then
  if command -v tailscaled >/dev/null 2>&1; then
    TS_STATE="$HOME_DIR/.local/share/slopbox/tailscale"
    mkdir -p "$TS_STATE" /var/run/tailscale
    tailscaled --tun=userspace-networking --state="$TS_STATE/state" \
      --socket=/var/run/tailscale/tailscaled.sock \
      >"$HOME_DIR/.local/share/slopbox/tailscaled.log" 2>&1 &
    sleep 1
    if [ -n "${TAILSCALE_KEY:-}" ]; then
      tailscale up --authkey="$TAILSCALE_KEY" --hostname="${SLOP_ID:-slopbox}" --accept-routes >/dev/null 2>&1 \
        && echo "slopbox: tailscale up as ${SLOP_ID:-slopbox} (tailnet)" \
        || echo "slopbox: ⚠ tailscale up failed"
    else
      echo "slopbox: tailscaled started — run 'sudo tailscale up --hostname=${SLOP_ID:-slopbox}' to log in"
    fi
  else
    echo "slopbox: ⚠ tailscale not installed"
  fi
fi

# 5. Run the app as the unprivileged user, with HOME + tool paths integrated so
#    directly-launched agents/editors see mise- and brew-installed tools.
export HOME="$HOME_DIR"
export PATH="$HOME_DIR/.local/share/mise/shims:/home/linuxbrew/.linuxbrew/bin:$HOME_DIR/.local/bin:/usr/local/bin:$PATH"
exec gosu "$USERNAME" slopbox "$@"
