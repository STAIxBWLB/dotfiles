#!/usr/bin/env bash
# ahub-pane.sh — what the ahub layouts and `dot hub` run in their slots (#331).
# It starts nothing by itself: the console opens only when the hub already
# runs, and the hub or a peer starts only when the person presses Enter.
#   ahub-pane.sh hub <root>
#   ahub-pane.sh peer <root> <id> <ahub args...>
set -uo pipefail

mode="${1:?usage: ahub-pane.sh hub|peer <root> ...}"
root="${2:?missing project root}"
shift 2

if ! command -v ahub >/dev/null 2>&1; then
  echo "ahub is not installed. Install it with: dot ai family install ahub"
  exit 0
fi

hub_running() { ahub --project "$root" status --json >/dev/null 2>&1; }

# The full console TUI needs 80x24; a smaller pane gets the plain console.
open_console() {
  local w=0 h=0
  read -r w h < <(tmux display-message -p -t "${TMUX_PANE:-}" '#{pane_width} #{pane_height}' 2>/dev/null) || true
  if (( ${w:-0} >= 80 && ${h:-0} >= 24 )); then
    ahub --project "$root" console --panels
  else
    echo "This pane is ${w}x${h}, below 80x24: opening the plain console."
    ahub --project "$root" console
  fi
}

case "$mode" in
  hub)
    while :; do
      if hub_running; then
        open_console
        echo "Console closed; the hub keeps running."
        echo "Enter: reopen the console  |  Ctrl-C: shell"
        read -r || exit 0
        continue
      fi
      echo "The hub for $root is not running."
      echo "Enter: start it without agents and open the console (ahub up --no-agents)  |  Ctrl-C: shell"
      read -r || exit 0
      ahub --project "$root" up --no-agents
    done
    ;;
  peer)
    id="${1:?missing peer id}"
    shift
    echo "Peer slot: $id"
    echo "Enter: start it (ahub --project $root $*)  |  Ctrl-C: shell"
    read -r || exit 0
    exec ahub --project "$root" "$@"
    ;;
  *)
    echo "ahub-pane.sh: unknown mode $mode" >&2
    exit 2
    ;;
esac
