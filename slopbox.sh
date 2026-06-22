#!/usr/bin/env bash
# slopbox.sh — manage slopbox dev boxes.
#
#   slopbox.sh start [dir]      build/run a box for a project (default: cwd)
#   slopbox.sh list             list all boxes (running + stopped + state-only)
#   slopbox.sh stop  [name]     stop a box (keeps it; `start` resumes it)
#   slopbox.sh ish   [name]     open an interactive shell inside a box
#   slopbox.sh open  [name]     open the box's web UI in your browser
#   slopbox.sh logs  [name]     follow a box's logs
#   slopbox.sh nuke  [name]     remove a box AND its persistent state
#
# start flags:  --rebuild  --fg  --port N  --docker  --pull  --image REF  --publish P
# nuke flags:   --image    (also remove the local slopbox image)
#
# When a box isn't named, the current dir's box is used, else fzf lets you pick.
set -euo pipefail

SLOPBOX_DIR="$HOME/.slopbox"
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

# --- box introspection -----------------------------------------------------
container_of() { echo "slopbox-$1"; }

# all box names: union of slopbox-* containers and ~/.slopbox/* state dirs
all_names() {
  { docker ps -a --filter "name=^slopbox-" --format '{{.Names}}' 2>/dev/null | sed 's/^slopbox-//'
    [ -d "$SLOPBOX_DIR" ] && ls -1 "$SLOPBOX_DIR" 2>/dev/null
  } | sort -u | grep -v '^$' || true
}

box_state() { # running | stopped | none
  local c; c="$(container_of "$1")"
  case "$(docker inspect -f '{{.State.Running}}' "$c" 2>/dev/null)" in
    true) echo running ;; false) echo stopped ;; *) echo none ;;
  esac
}

box_port() { # works for running and stopped containers
  docker inspect -f '{{with index .HostConfig.PortBindings "7000/tcp"}}{{(index . 0).HostPort}}{{end}}' "$(container_of "$1")" 2>/dev/null || true
}

box_project() {
  local p; p="$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/work"}}{{.Source}}{{end}}{{end}}' "$(container_of "$1")" 2>/dev/null || true)"
  [ -z "$p" ] && [ -f "$SLOPBOX_DIR/$1/project" ] && p="$(cat "$SLOPBOX_DIR/$1/project")"
  echo "$p"
}

box_size() { du -sh "$SLOPBOX_DIR/$1" 2>/dev/null | cut -f1 || echo "-"; }

# resolve a box name from an arg, the current dir, or an fzf pick
resolve_box() {
  local want="${1:-}"
  [ -n "$want" ] && { echo "$want"; return; }
  local cur; cur="$(basename "$PWD")"
  [ "$(box_state "$cur")" != none ] || [ -d "$SLOPBOX_DIR/$cur" ] && { echo "$cur"; return; }
  pick_box
}

pick_box() {
  local names cnt
  names="$(all_names)"; cnt="$(printf '%s\n' "$names" | grep -c . || true)"
  [ "$cnt" -eq 0 ] && die "no boxes yet — run 'slopbox.sh start'"
  if command -v fzf >/dev/null 2>&1; then
    printf '%s\n' "$names" | fzf --prompt="box ❯ " --height=45% --reverse \
      --preview "'$ROOT/slopbox.sh' _preview {}" --preview-window=right,55% \
      || die "no box selected"
  elif [ "$cnt" -eq 1 ]; then
    printf '%s\n' "$names"
  else
    echo "multiple boxes — name one (or install fzf):" >&2
    printf '  %s\n' $names >&2; exit 1
  fi
}

# --- commands --------------------------------------------------------------
cmd_list() {
  need_docker
  local names; names="$(all_names)"
  [ -z "$names" ] && { echo "${C_DIM}no boxes yet — run 'slopbox.sh start'${C_RST}"; return; }
  printf "${C_B}%-16s %-9s %-7s %-7s %s${C_RST}\n" "BOX" "STATUS" "PORT" "STATE" "PROJECT"
  local n st dot port proj size
  while IFS= read -r n; do
    [ -z "$n" ] && continue
    st="$(box_state "$n")"; port="$(box_port "$n")"; proj="$(box_project "$n")"; size="$(box_size "$n")"
    case "$st" in
      running) dot="${C_GRN}●${C_RST}"; st="${C_GRN}up${C_RST}     " ;;
      stopped) dot="${C_YEL}○${C_RST}"; st="${C_YEL}stopped${C_RST}" ;;
      *)       dot="${C_DIM}○${C_RST}"; st="${C_DIM}—${C_RST}      " ;;
    esac
    printf "%b %-14s %b %-7s %-7s ${C_DIM}%s${C_RST}\n" "$dot" "$n" "$st" "${port:--}" "$size" "${proj:--}"
  done <<< "$names"
}

cmd_preview() { # internal: fzf preview pane
  local n="$1"
  echo "${C_B}${C_PUR}$n${C_RST}"
  echo "${C_DIM}status${C_RST}  $(box_state "$n")"
  echo "${C_DIM}port${C_RST}    $(box_port "$n")"
  echo "${C_DIM}project${C_RST} $(box_project "$n")"
  echo "${C_DIM}state${C_RST}   $SLOPBOX_DIR/$n ($(box_size "$n"))"
  echo "${C_DIM}─── recent logs ───${C_RST}"
  docker logs --tail 10 "$(container_of "$n")" 2>&1 | sed 's/\x1b\[[0-9;]*m//g' || true
}

cmd_start() {
  need_docker
  local image="$IMAGE_DEFAULT" port="7700" rebuild=0 docksock=0 pull=0 fg=0 project=""
  local -a extra=()
  while [ $# -gt 0 ]; do
    case "$1" in
      --rebuild) rebuild=1; shift ;;
      --port) port="${2#:}"; shift 2 ;;
      --docker) docksock=1; shift ;;
      --pull) pull=1; shift ;;
      --fg|--foreground) fg=1; shift ;;
      --image) image="$2"; shift 2 ;;
      --publish) extra+=("$2"); shift 2 ;;
      -*) die "unknown start flag: $1" ;;
      *) project="$1"; shift ;;
    esac
  done
  project="${project:-$PWD}"; project="$(cd "$project" && pwd)" || die "no such dir"
  git -C "$project" rev-parse --is-inside-work-tree >/dev/null 2>&1 || die "$project is not a git repo"
  local name container state
  name="$(basename "$project")"; container="$(container_of "$name")"
  state="$SLOPBOX_DIR/$name/home"; mkdir -p "$state"
  echo "$project" > "$SLOPBOX_DIR/$name/project"

  if [ "$pull" -eq 1 ]; then
    image="ghcr.io/jclement/slopbox:latest"; docker pull "$image"
  elif [ "$rebuild" -eq 1 ] || ! docker image inspect "$image" >/dev/null 2>&1; then
    echo "${C_PUR}»${C_RST} building $image"; docker build -t "$image" "$ROOT"
  fi

  local -a mounts=(
    --name "$container"
    -v "$project:/work"
    -v "$state:/home/ubuntu"
    -v "slopbox-${name}-pg:/home/ubuntu/.local/share/slopbox/postgres"
    -p "$port:7000" -e "SLOPBOX_REPO=/work"
  )
  [ "$docksock" -eq 1 ] && mounts+=(-v /var/run/docker.sock:/var/run/docker.sock)
  for p in ${extra[@]+"${extra[@]}"}; do mounts+=(-p "$p:$p"); done

  # Foreground + ephemeral (mise run dev): stream logs live, ctrl-C stops &
  # removes the container. The state volume/mount persists either way.
  if [ "$fg" -eq 1 ]; then
    docker rm -f "$container" >/dev/null 2>&1 || true
    local -a tty=(); [ -t 0 ] && tty=(-it)
    echo "${C_GRN}●${C_RST} open ${C_B}http://localhost:${port}/${C_RST}  ${C_DIM}(bootstrap code below; ctrl-C stops & removes)${C_RST}"
    exec docker run --rm "${tty[@]}" "${mounts[@]}" "$image"
  fi

  # Detached: reuse an existing container unless rebuilding/pulling.
  case "$(box_state "$name")" in
    running) echo "${C_GRN}●${C_RST} $name already running"; print_access "$name" "$port"; return ;;
    stopped)
      if [ "$rebuild" -eq 1 ] || [ "$pull" -eq 1 ]; then docker rm -f "$container" >/dev/null
      else echo "${C_PUR}»${C_RST} resuming $name"; docker start "$container" >/dev/null; print_access "$name" "$(box_port "$name")"; return; fi ;;
  esac
  echo "${C_PUR}»${C_RST} starting $name ${C_DIM}($project)${C_RST}"
  docker run -d "${mounts[@]}" "$image" >/dev/null
  sleep 2; print_access "$name" "$port"
}

print_access() {
  local name="$1" port="$2" boot
  echo "${C_GRN}●${C_RST} open   ${C_B}http://localhost:${port}/${C_RST}"
  boot="$(docker logs "$(container_of "$name")" 2>&1 | grep -oE 'bootstrap code: [A-Z0-9-]+' | tail -1 | sed 's/bootstrap code: //' || true)"
  [ -n "$boot" ] && echo "  ${C_DIM}first run? register a passkey with code:${C_RST} ${C_YEL}$boot${C_RST}"
  echo "  ${C_DIM}ish:${C_RST} slopbox.sh ish $name   ${C_DIM}logs:${C_RST} slopbox.sh logs $name"
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
  echo "${C_RED}✗ nuking${C_RST} $n: container + ${C_DIM}$SLOPBOX_DIR/$n${C_RST} + volume slopbox-${n}-pg"
  docker rm -f "$(container_of "$n")" >/dev/null 2>&1 || true
  docker volume rm "slopbox-${n}-pg" >/dev/null 2>&1 || true
  rm -rf "${SLOPBOX_DIR:?}/$n"
  if [ "$rmimage" -eq 1 ]; then echo "  ${C_DIM}removing image $IMAGE_DEFAULT${C_RST}"; docker rmi "$IMAGE_DEFAULT" >/dev/null 2>&1 || true; fi
  echo "${C_GRN}●${C_RST} done"
}

usage() { sed -n '2,18p' "$0" | sed 's/^# \{0,1\}//'; exit "${1:-0}"; }

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
