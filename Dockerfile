# slopbox — single immutable image for sandboxed vibe coding.
#
# Layering rule: binaries live in the image (/usr/local/bin, apt); everything
# YOU add at runtime (mise/brew installs, agent logins, keys, postgres data)
# lands in the persistent home mount at /home/ubuntu. Because that mount SHADOWS
# the image's /home/ubuntu, base-image tools never live in home — dotfiles are
# seeded from /etc/skel by the entrypoint on first run.

# --- build the Go web app -------------------------------------------------
FROM golang:1.26 AS app
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/slopbox ./cmd/slopbox

# --- runtime ---------------------------------------------------------------
FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    SLOPBOX_REPO=/work \
    SLOPBOX_WORKTREES=/home/ubuntu/worktrees \
    PGDATA=/home/ubuntu/.local/share/slopbox/postgres

# Root-level system packages (incl. postgres server + client).
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates curl wget gnupg git tmux zsh gosu openssh-server \
        build-essential procps iproute2 file locales sudo unzip xz-utils \
        vim nodejs npm software-properties-common \
        postgresql postgresql-contrib \
    && rm -rf /var/lib/apt/lists/*

# helix editor (community PPA — no official Ubuntu package yet).
RUN add-apt-repository -y ppa:maveonair/helix-editor \
    && apt-get update && apt-get install -y --no-install-recommends helix \
    && rm -rf /var/lib/apt/lists/*

# mise — dev-tool/version manager (binary in image; its installs go to home).
RUN curl -fsSL https://mise.run | sh -s -- --yes \
    && mv /root/.local/bin/mise /usr/local/bin/mise

# starship prompt (installs to /usr/local/bin).
RUN curl -fsSL https://starship.rs/install.sh | sh -s -- -y

# Fresh terminal IDE (getfresh.dev) + coding agents, system-wide via npm so
# they survive the persistent-home mount. Agents run in danger mode.
RUN npm install -g @fresh-editor/fresh-editor @anthropic-ai/claude-code @openai/codex \
    && npm cache clean --force

# pgweb — browser DB UI, proxied at /db (runs with --prefix=db).
RUN arch="$(dpkg --print-architecture)" \
    && curl -fsSL "https://github.com/sosedoff/pgweb/releases/download/v0.17.0/pgweb_linux_${arch}.zip" -o /tmp/pgweb.zip \
    && unzip -q /tmp/pgweb.zip -d /tmp \
    && mv "/tmp/pgweb_linux_${arch}" /usr/local/bin/pgweb \
    && chmod +x /usr/local/bin/pgweb && rm /tmp/pgweb.zip

# Tailscale — private-mesh remote access (the box gets its own tailnet IP +
# MagicDNS name, every port reachable, no public exposure). Started by the
# entrypoint only when TAILSCALE_KEY / SLOPBOX_TAILSCALE is set.
RUN arch="$(dpkg --print-architecture)" \
    && ver="$(curl -fsSL https://pkgs.tailscale.com/stable/ | grep -oE "tailscale_[0-9.]+_${arch}\.tgz" | head -1 | sed -E 's/tailscale_([0-9.]+)_.*/\1/')" \
    && curl -fsSL "https://pkgs.tailscale.com/stable/tailscale_${ver}_${arch}.tgz" -o /tmp/ts.tgz \
    && tar -xzf /tmp/ts.tgz -C /tmp \
    && mv "/tmp/tailscale_${ver}_${arch}/tailscale" "/tmp/tailscale_${ver}_${arch}/tailscaled" /usr/local/bin/ \
    && rm -rf /tmp/ts.tgz "/tmp/tailscale_${ver}_${arch}"

# code-server (/edit) is NOT baked in — it's downloaded into the persistent home
# (/home/ubuntu/.local) on first use by the entrypoint, so the image stays lean.

# Static docker CLI — talk to a passed-through host docker socket. No daemon,
# no docker-in-docker.
ARG DOCKER_VERSION=27.3.1
RUN arch="$(uname -m)" \
    && curl -fsSL "https://download.docker.com/linux/static/stable/${arch}/docker-${DOCKER_VERSION}.tgz" -o /tmp/d.tgz \
    && tar -xzf /tmp/d.tgz -C /tmp \
    && mv /tmp/docker/docker /usr/local/bin/docker \
    && rm -rf /tmp/d.tgz /tmp/docker

# Run as the stock 'ubuntu' user (uid 1000): zsh + passwordless sudo.
RUN chsh -s /usr/bin/zsh ubuntu \
    && echo 'ubuntu ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/ubuntu \
    # /work is bind-mounted with the host's uid; allow git despite foreign owner.
    && git config --system --add safe.directory '*'
COPY docker/skel/.zshrc /etc/skel/.zshrc

# slopbox binary + entrypoint.
COPY --from=app /out/slopbox /usr/local/bin/slopbox
COPY docker/entrypoint.sh /usr/local/bin/slopbox-entrypoint
RUN chmod +x /usr/local/bin/slopbox-entrypoint

WORKDIR /work
EXPOSE 7000
ENTRYPOINT ["slopbox-entrypoint"]
