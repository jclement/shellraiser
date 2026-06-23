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
  echo "slopbox: installing code-server into the home volume (first run)…"
  run_user sh -c 'curl -fsSL https://code-server.dev/install.sh | sh -s -- --method standalone --prefix "$HOME/.local"'
}
ensure_cloudflared() {
  [ -x "$HOME_DIR/.local/bin/cloudflared" ] && return 0
  echo "slopbox: downloading cloudflared…"
  run_user sh -c "mkdir -p \$HOME/.local/bin && curl -fsSL 'https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-$(dpkg --print-architecture)' -o \$HOME/.local/bin/cloudflared && chmod +x \$HOME/.local/bin/cloudflared"
}
ensure_gatecrash() {
  [ -x "$HOME_DIR/.local/bin/gatecrash" ] && return 0
  echo "slopbox: downloading gatecrash client…"
  run_user sh -c "mkdir -p \$HOME/.local/bin && curl -fsSL 'https://github.com/jclement/gatecrash/releases/latest/download/gatecrash_linux_$(dpkg --print-architecture)' -o \$HOME/.local/bin/gatecrash && chmod +x \$HOME/.local/bin/gatecrash"
}

# 1. Seed dotfiles into the (possibly empty) persistent home mount.
if [ ! -f "$HOME_DIR/.zshrc" ] && [ -f /etc/skel/.zshrc ]; then
  cp /etc/skel/.zshrc "$HOME_DIR/.zshrc"
fi
mkdir -p "$HOME_DIR/worktrees" "$HOME_DIR/.local/bin" "$HOME_DIR/.config" \
         "$HOME_DIR/.local/share/slopbox" "$HOME_DIR/linuxbrew"

# Keep Homebrew's data under the home mount while preserving the standard prefix
# path (so prebuilt bottles still apply): symlink /home/linuxbrew → home mount.
if [ ! -L /home/linuxbrew ]; then
  rm -rf /home/linuxbrew
  ln -s "$HOME_DIR/linuxbrew" /home/linuxbrew
fi
chown -R "$USERNAME:$USERNAME" "$HOME_DIR"

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

if [ "${SLOPBOX_POSTGRES:-1}" != "0" ]; then
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

# 2c. code-server at /edit (default on; SLOPBOX_CODE_SERVER=0 to disable).
if [ "${SLOPBOX_CODE_SERVER:-1}" != "0" ] && ensure_code_server; then
  CS_DIR="$HOME_DIR/.local/share/code-server"
  mkdir -p "$CS_DIR/extensions" "$CS_DIR/User"
  # Disable the welcome/getting-started tab + telemetry (only seed once).
  if [ ! -f "$CS_DIR/User/settings.json" ]; then
    printf '{\n  "workbench.startupEditor": "none",\n  "telemetry.telemetryLevel": "off"\n}\n' > "$CS_DIR/User/settings.json"
  fi
  chown -R "$USERNAME:$USERNAME" "$CS_DIR"
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
fi

# 3. If the docker socket was passed through, let 'ubuntu' use it.
if [ -S /var/run/docker.sock ]; then
  sock_gid="$(stat -c '%g' /var/run/docker.sock)"
  if ! getent group "$sock_gid" >/dev/null 2>&1; then
    groupadd -g "$sock_gid" hostdocker || true
  fi
  usermod -aG "$sock_gid" "$USERNAME" || true
fi

# 4. Optional external access, selected by which env vars are present. The
#    tunnel binary is downloaded into the home volume on first use.
if [ -n "${CLOUDFLARE_TUNNEL_TOKEN:-}" ] && ensure_cloudflared; then
  echo "slopbox: starting cloudflared tunnel"
  run_user cloudflared tunnel --no-autoupdate run --token "$CLOUDFLARE_TUNNEL_TOKEN" &
elif [ -n "${GATECRASH_SERVER:-}" ] && [ -n "${GATECRASH_TOKEN:-}" ] && ensure_gatecrash; then
  echo "slopbox: starting gatecrash tunnel → $GATECRASH_SERVER"
  run_user gatecrash --server "$GATECRASH_SERVER" --token "$GATECRASH_TOKEN" \
    ${GATECRASH_HOST_KEY:+--host-key "$GATECRASH_HOST_KEY"} \
    --target "127.0.0.1:${PORT}" &
elif [ -f /etc/gatecrash/client.toml ] && ensure_gatecrash; then
  echo "slopbox: starting gatecrash from /etc/gatecrash/client.toml"
  run_user gatecrash &
fi

# 5. Run the app as the unprivileged user, with HOME + tool paths integrated so
#    directly-launched agents/editors see mise- and brew-installed tools.
export HOME="$HOME_DIR"
export PATH="$HOME_DIR/.local/share/mise/shims:/home/linuxbrew/.linuxbrew/bin:$HOME_DIR/.local/bin:/usr/local/bin:$PATH"
exec gosu "$USERNAME" slopbox "$@"
