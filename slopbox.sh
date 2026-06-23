#!/usr/bin/env bash
# slopbox.sh — manage slopbox dev boxes.
#
#   slopbox.sh start [dir]      build/run a box for a project (default: cwd)
#   slopbox.sh list             list all boxes (running + stopped)
#   slopbox.sh stop  [id]       stop a box (keeps it; `start` resumes it)
#   slopbox.sh ish   [id]       open an interactive shell inside a box
#   slopbox.sh open  [id]       open the box's web UI in your browser
#   slopbox.sh logs  [id]       follow a box's logs
#   slopbox.sh nuke  [id]       remove a box AND its persistent volume
#
# A box's identity (SLOP_ID) is the project folder name, or `id` in .slopbox.toml,
# or --id. Container = sb_<id>; one persistent named volume = sb_<id>_vol.
#
# start flags:  --id NAME  --rebuild  --fg  --port N  --docker  --pull  --publish P
#               --ssh [--ssh-port N] [--ssh-key PATH]   (key-only ssh + port fwd)
#               --tailscale                              (start tailscaled; sudo tailscale up)
# Tailscale: export TAILSCALE_KEY to auto-join the tailnet as <id>.
# nuke flags:   --image    (also remove the local slopbox image)
#
# When a box isn't named, the current dir's box is used, else fzf lets you pick.
set -euo pipefail

IMAGE_DEFAULT="slopbox:local"
ROOT="$(cd "$(dirname "$0")" && pwd)"

# --- colors ----------------------------------------------------------------
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  C_DIM=$'\e[2m'; C_GRN=$'\e[38;5;79m'; C_RED=$'\e[38;5;203m'; C_PUR=$'\e[38;5;141m'
  C_YEL=$'\e[38;5;221m'; C_BLU=$'\e[38;5;75m'; C_RST=$'\e[0m'; C_B=$'\e[1m'
else
  C_DIM=; C_GRN=; C_RED=; C_PUR=; C_YEL=; C_BLU=; C_RST=; C_B=
fi

die() { echo "${C_RED}error:${C_RST} $*" >&2; exit 1; }
need_docker() { command -v docker >/dev/null 2>&1 || die "docker not found"; }

# --- box identity + introspection ------------------------------------------
container_of() { echo "sb_$1"; }
volume_of()    { echo "sb_${1}_vol"; }

# Box id for a project dir: `id` in .slopbox.toml/.local, else the folder name.
config_id() {
  local proj="$1" f line id=""
  for f in "$proj/.slopbox.toml" "$proj/.slopbox.local.toml"; do
    [ -f "$f" ] || continue
    line="$(grep -E '^[[:space:]]*id[[:space:]]*=' "$f" 2>/dev/null | head -1 || true)"
    [ -n "$line" ] && id="$(printf '%s' "${line#*=}" | tr -d "\"' ")"
  done
  [ -n "$id" ] && printf '%s\n' "$id" || basename "$proj"
}

# All box ids: union of sb_* containers and sb_*_vol volumes.
all_ids() {
  { docker ps -a --filter "name=^sb_" --format '{{.Names}}' 2>/dev/null | sed 's/^sb_//'
    docker volume ls --filter "name=^sb_" --format '{{.Name}}' 2>/dev/null | sed -e 's/^sb_//' -e 's/_vol$//'
  } | sort -u | grep -v '^$' || true
}

box_state() { # running | stopped | none
  case "$(docker inspect -f '{{.State.Running}}' "$(container_of "$1")" 2>/dev/null)" in
    true) echo running ;; false) echo stopped ;; *) echo none ;;
  esac
}
box_port() {
  docker inspect -f '{{with index .HostConfig.PortBindings "7000/tcp"}}{{(index . 0).HostPort}}{{end}}' "$(container_of "$1")" 2>/dev/null || true
}
box_project() {
  local c p; c="$(container_of "$1")"
  p="$(docker inspect -f '{{index .Config.Labels "slopbox.project"}}' "$c" 2>/dev/null || true)"
  [ -z "$p" ] && p="$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/work"}}{{.Source}}{{end}}{{end}}' "$c" 2>/dev/null || true)"
  echo "$p"
}

resolve_box() {
  local want="${1:-}"
  [ -n "$want" ] && { echo "$want"; return; }
  local cur; cur="$(config_id "$PWD")"
  if [ "$(box_state "$cur")" != none ] || docker volume inspect "$(volume_of "$cur")" >/dev/null 2>&1; then
    echo "$cur"; return
  fi
  pick_box
}

pick_box() {
  local ids cnt
  ids="$(all_ids)"; cnt="$(printf '%s\n' "$ids" | grep -c . || true)"
  [ "$cnt" -eq 0 ] && die "no boxes yet — run 'slopbox.sh start'"
  if command -v fzf >/dev/null 2>&1; then
    printf '%s\n' "$ids" | fzf --prompt="box ❯ " --height=45% --reverse \
      --preview "'$ROOT/slopbox.sh' _preview {}" --preview-window=right,55% || die "no box selected"
  elif [ "$cnt" -eq 1 ]; then
    printf '%s\n' "$ids"
  else
    echo "multiple boxes — name one (or install fzf):" >&2
    printf '  %s\n' $ids >&2; exit 1
  fi
}

# --- commands --------------------------------------------------------------
cmd_list() {
  need_docker
  local ids; ids="$(all_ids)"
  [ -z "$ids" ] && { echo "${C_DIM}no boxes yet — run 'slopbox.sh start'${C_RST}"; return; }
  printf "${C_B}%-18s %-9s %-7s %s${C_RST}\n" "BOX" "STATUS" "PORT" "PROJECT"
  local n st dot port proj
  while IFS= read -r n; do
    [ -z "$n" ] && continue
    st="$(box_state "$n")"; port="$(box_port "$n")"; proj="$(box_project "$n")"
    case "$st" in
      running) dot="${C_GRN}●${C_RST}"; st="${C_GRN}up${C_RST}     " ;;
      stopped) dot="${C_YEL}○${C_RST}"; st="${C_YEL}stopped${C_RST}" ;;
      *)       dot="${C_DIM}○${C_RST}"; st="${C_DIM}—${C_RST}      " ;;
    esac
    printf "%b %-16s %b %-7s ${C_DIM}%s${C_RST}\n" "$dot" "$n" "$st" "${port:--}" "${proj:--}"
  done <<< "$ids"
}

cmd_preview() {
  local n="$1"
  echo "${C_B}${C_PUR}$n${C_RST}"
  echo "${C_DIM}status${C_RST}  $(box_state "$n")"
  echo "${C_DIM}port${C_RST}    $(box_port "$n")"
  echo "${C_DIM}project${C_RST} $(box_project "$n")"
  echo "${C_DIM}volume${C_RST}  $(volume_of "$n")"
  echo "${C_DIM}─── recent logs ───${C_RST}"
  docker logs --tail 10 "$(container_of "$n")" 2>&1 | sed 's/\x1b\[[0-9;]*m//g' || true
}

# Expand the `ports` setting from a project's .slopbox.toml/.local into host
# ports to publish. Ranges are expanded but CAPPED — a huge range makes Docker
# reserve a proxy per port (slow, heavy). Use /p/ or ssh -L for large sets.
config_ports() {
  local proj="$1" spec="" f line cap=128
  for f in "$proj/.slopbox.toml" "$proj/.slopbox.local.toml"; do
    [ -f "$f" ] || continue
    line="$(grep -E '^[[:space:]]*ports[[:space:]]*=' "$f" 2>/dev/null | head -1 || true)"
    [ -n "$line" ] && spec="$line"
  done
  [ -n "$spec" ] || return 0
  spec="$(printf '%s' "${spec#*=}" | tr -d "[]\"' ")"
  local -a out=(); local p n lo hi
  for p in $(printf '%s' "$spec" | tr ',' ' '); do
    [ -z "$p" ] && continue
    case "$p" in
      *-*) lo="${p%-*}"; hi="${p#*-}"; for ((n=lo; n<=hi; n++)); do [ "${#out[@]}" -ge "$cap" ] && break; out+=("$n"); done ;;
      *)   out+=("$p") ;;
    esac
  done
  [ "${#out[@]}" -ge "$cap" ] && echo "${C_YEL}» ports capped at $cap — use /p/ or 'ssh -L' for large sets${C_RST}" >&2
  printf '%s\n' ${out[@]+"${out[@]}"}
}

cmd_start() {
  need_docker
  local image="$IMAGE_DEFAULT" port="7700" rebuild=0 docksock=0 pull=0 fg=0 ssh=0 sshport="2222" sshkey="" ts=0 id="" project=""
  local -a extra=()
  while [ $# -gt 0 ]; do
    case "$1" in
      --id) id="$2"; shift 2 ;;
      --rebuild) rebuild=1; shift ;;
      --port) port="${2#:}"; shift 2 ;;
      --docker) docksock=1; shift ;;
      --pull) pull=1; shift ;;
      --fg|--foreground) fg=1; shift ;;
      --ssh) ssh=1; shift ;;
      --ssh-port) ssh=1; sshport="${2#:}"; shift 2 ;;
      --ssh-key) ssh=1; sshkey="$2"; shift 2 ;;
      --tailscale) ts=1; shift ;;
      --image) image="$2"; shift 2 ;;
      --publish) extra+=("$2"); shift 2 ;;
      -*) die "unknown start flag: $1" ;;
      *) project="$1"; shift ;;
    esac
  done
  project="${project:-$PWD}"; project="$(cd "$project" && pwd)" || die "no such dir"
  git -C "$project" rev-parse --is-inside-work-tree >/dev/null 2>&1 || die "$project is not a git repo"
  [ -n "$id" ] || id="$(config_id "$project")"
  local container vol; container="$(container_of "$id")"; vol="$(volume_of "$id")"

  if [ "$pull" -eq 1 ]; then
    image="ghcr.io/jclement/slopbox:latest"; docker pull "$image"
  elif [ "$rebuild" -eq 1 ] || ! docker image inspect "$image" >/dev/null 2>&1; then
    echo "${C_PUR}»${C_RST} building $image"; docker build -t "$image" "$ROOT"
  fi

  # One persistent named volume per box holds ALL state (tools, logins,
  # worktrees, postgres data). The project is bind-mounted at /work.
  local -a mounts=(
    --name "$container"
    --label "slopbox.id=$id" --label "slopbox.project=$project"
    -v "$project:/work"
    -v "$vol:/home/ubuntu"
    -p "$port:7000"
    -e "SLOPBOX_REPO=/work" -e "SLOP_ID=$id"
  )
  [ "$docksock" -eq 1 ] && mounts+=(-v /var/run/docker.sock:/var/run/docker.sock)
  for p in ${extra[@]+"${extra[@]}"}; do mounts+=(-p "$p:$p"); done
  while IFS= read -r p; do [ -n "$p" ] && mounts+=(-p "$p:$p"); done < <(config_ports "$project")

  # SSH (key-only) with port forwarding: publish host $sshport → container :22.
  # A pre-shared key is optional — the web UI's "Copy SSH" button mints ephemeral
  # keys on demand. If you have one, it's added as a persistent authorized key.
  if [ "$ssh" -eq 1 ]; then
    mounts+=(-p "$sshport:22" -e SLOPBOX_SSH=1 -e "SLOPBOX_SSH_HOST_PORT=$sshport")
    local key="$sshkey"
    [ -z "$key" ] && for k in "$HOME/.ssh/id_ed25519.pub" "$HOME/.ssh/id_rsa.pub"; do [ -f "$k" ] && key="$k" && break; done
    [ -n "$key" ] && [ -f "$key" ] && mounts+=(-e "SLOPBOX_SSH_PUBKEY=$(cat "$key")")
  fi
  # Tailscale: auto-join with TAILSCALE_KEY, or --tailscale for manual login.
  [ -n "${TAILSCALE_KEY:-}" ] && mounts+=(-e "TAILSCALE_KEY=$TAILSCALE_KEY")
  [ "$ts" -eq 1 ] && mounts+=(-e SLOPBOX_TAILSCALE=1)

  if [ "$fg" -eq 1 ]; then
    docker rm -f "$container" >/dev/null 2>&1 || true
    local -a tty=(); [ -t 0 ] && tty=(-it)
    echo "${C_GRN}●${C_RST} open ${C_B}http://localhost:${port}/${C_RST}  ${C_DIM}(ctrl-C stops & removes; volume persists)${C_RST}"
    exec docker run --rm "${tty[@]}" "${mounts[@]}" "$image"
  fi

  case "$(box_state "$id")" in
    running) echo "${C_GRN}●${C_RST} $id already running"; print_access "$id" "$port" "$ssh" "$sshport"; return ;;
    stopped)
      if [ "$rebuild" -eq 1 ] || [ "$pull" -eq 1 ]; then docker rm -f "$container" >/dev/null
      else echo "${C_PUR}»${C_RST} resuming $id"; docker start "$container" >/dev/null; print_access "$id" "$(box_port "$id")" "$ssh" "$sshport"; return; fi ;;
  esac
  echo "${C_PUR}»${C_RST} starting $id ${C_DIM}($project)${C_RST}"
  docker run -d "${mounts[@]}" "$image" >/dev/null
  sleep 2; print_access "$id" "$port" "$ssh" "$sshport"
}

print_access() {
  local id="$1" port="$2" ssh="${3:-0}" sshport="${4:-}" boot
  echo "${C_GRN}●${C_RST} open   ${C_B}http://localhost:${port}/${C_RST}"
  boot="$(docker logs "$(container_of "$id")" 2>&1 | grep -oE 'bootstrap code: [A-Z0-9-]+' | tail -1 | sed 's/bootstrap code: //' || true)"
  [ -n "$boot" ] && echo "  ${C_DIM}first run? register a passkey with code:${C_RST} ${C_YEL}$boot${C_RST}"
  [ "$ssh" = "1" ] && echo "  ${C_DIM}ssh:${C_RST}  ssh ubuntu@localhost -p $sshport   ${C_DIM}(-L 3000:localhost:3000 to forward)${C_RST}"
  echo "  ${C_DIM}ish:${C_RST} slopbox.sh ish $id   ${C_DIM}logs:${C_RST} slopbox.sh logs $id"
}

cmd_stop() {
  need_docker; local n; n="$(resolve_box "${1:-}")"
  [ "$(box_state "$n")" = running ] || die "$n is not running"
  echo "${C_YEL}○${C_RST} stopping $n"; docker stop "$(container_of "$n")" >/dev/null
}

cmd_ish() {
  need_docker; local n; n="$(resolve_box "${1:-}")"
  [ "$(box_state "$n")" = running ] || die "$n is not running (start it first)"
  echo "${C_PUR}»${C_RST} shell into $n ${C_DIM}(ctrl-d to exit)${C_RST}"
  exec docker exec -it -u ubuntu -e HOME=/home/ubuntu -w /work "$(container_of "$n")" zsh -l
}

cmd_open() {
  need_docker; local n port; n="$(resolve_box "${1:-}")"; port="$(box_port "$n")"
  [ -n "$port" ] || die "$n has no published port"
  local url="http://localhost:$port/"
  command -v open >/dev/null 2>&1 && open "$url" || command -v xdg-open >/dev/null 2>&1 && xdg-open "$url" || echo "$url"
}

cmd_logs() {
  need_docker; local n; n="$(resolve_box "${1:-}")"
  exec docker logs -f "$(container_of "$n")"
}

cmd_nuke() {
  need_docker
  local rmimage=0 name=""
  while [ $# -gt 0 ]; do case "$1" in --image) rmimage=1; shift ;; *) name="$1"; shift ;; esac; done
  local n; n="$(resolve_box "$name")"
  echo "${C_RED}✗ nuking${C_RST} $n: container $(container_of "$n") + volume $(volume_of "$n")"
  docker rm -f "$(container_of "$n")" >/dev/null 2>&1 || true
  docker volume rm "$(volume_of "$n")" >/dev/null 2>&1 || true
  if [ "$rmimage" -eq 1 ]; then echo "  ${C_DIM}removing image $IMAGE_DEFAULT${C_RST}"; docker rmi "$IMAGE_DEFAULT" >/dev/null 2>&1 || true; fi
  echo "${C_GRN}●${C_RST} done"
}

usage() { sed -n '2,22p' "$0" | sed 's/^# \{0,1\}//'; exit "${1:-0}"; }

main() {
  local cmd="${1:-}"; shift || true
  case "$cmd" in
    start|up)      cmd_start "$@" ;;
    list|ls)       cmd_list ;;
    stop|down)     cmd_stop "$@" ;;
    ish|shell|sh)  cmd_ish "$@" ;;
    open)          cmd_open "$@" ;;
    logs)          cmd_logs "$@" ;;
    nuke|rm)       cmd_nuke "$@" ;;
    _preview)      cmd_preview "$@" ;;
    ""|help|-h|--help) usage 0 ;;
    *) echo "unknown command: $cmd" >&2; usage 1 ;;
  esac
}
main "$@"
