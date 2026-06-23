# shellraiser base image — ubuntu:24.04 + the full default toolchain. Built once by
# `sr` (tagged sr-base:<ver>) and reused as the default base for the lean overlay.
# Heavy but layer-cached across every project; the worker binary is NOT here (it
# lands in the overlay's final layer so a worker bump rebuilds almost nothing).
FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8

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

# starship prompt.
RUN curl -fsSL https://starship.rs/install.sh | sh -s -- -y

# Fresh terminal IDE + coding agents, system-wide so they survive the home mount.
RUN npm install -g @fresh-editor/fresh-editor @anthropic-ai/claude-code @openai/codex \
    && npm cache clean --force

# pgweb — browser DB UI, proxied at /db.
RUN arch="$(dpkg --print-architecture)" \
    && curl -fsSL "https://github.com/sosedoff/pgweb/releases/download/v0.17.0/pgweb_linux_${arch}.zip" -o /tmp/pgweb.zip \
    && unzip -q /tmp/pgweb.zip -d /tmp \
    && mv "/tmp/pgweb_linux_${arch}" /usr/local/bin/pgweb \
    && chmod +x /usr/local/bin/pgweb && rm /tmp/pgweb.zip

# Tailscale static binaries (used only if a worker opts into in-container TS).
RUN arch="$(dpkg --print-architecture)" \
    && ver="$(curl -fsSL https://pkgs.tailscale.com/stable/ | grep -oE "tailscale_[0-9.]+_${arch}\.tgz" | head -1 | sed -E 's/tailscale_([0-9.]+)_.*/\1/')" \
    && curl -fsSL "https://pkgs.tailscale.com/stable/tailscale_${ver}_${arch}.tgz" -o /tmp/ts.tgz \
    && tar -xzf /tmp/ts.tgz -C /tmp \
    && mv "/tmp/tailscale_${ver}_${arch}/tailscale" "/tmp/tailscale_${ver}_${arch}/tailscaled" /usr/local/bin/ \
    && rm -rf /tmp/ts.tgz "/tmp/tailscale_${ver}_${arch}"

# Static docker CLI — talk to a passed-through host docker socket (opt-in).
ARG DOCKER_VERSION=27.3.1
RUN arch="$(uname -m)" \
    && curl -fsSL "https://download.docker.com/linux/static/stable/${arch}/docker-${DOCKER_VERSION}.tgz" -o /tmp/d.tgz \
    && tar -xzf /tmp/d.tgz -C /tmp \
    && mv /tmp/docker/docker /usr/local/bin/docker \
    && rm -rf /tmp/d.tgz /tmp/docker

# Run as the stock 'ubuntu' user (uid 1000): zsh + passwordless sudo.
RUN chsh -s /usr/bin/zsh ubuntu \
    && echo 'ubuntu ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/ubuntu \
    && git config --system --add safe.directory '*'
COPY zshrc /etc/skel/.zshrc
