#!/usr/bin/env bash
# layouts.sh — Tmux workspace layout definitions for dotfiles workspace

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/tools.sh"
source "$SCRIPT_DIR/themes.sh"

# ── Layout: dev (default, 5-pane, laptop-friendly) ──────────────────────────
# ┌──────────────┬──────────┐
# │              │  MONITOR │
# │   CLAUDE     ├──────────┤
# │              │  FILES   │
# ├──────────────┼──────────┤
# │  LAZYGIT     │   SHELL  │
# └──────────────┴──────────┘
layout_dev() {
  local session="$1" project="$2" theme="$3"

  # Create session — first pane becomes CLAUDE
  tmux new-session -d -s "$session" -c "$project" -x 220 -y 60
  local CLAUDE
  CLAUDE=$(tmux list-panes -t "$session" -F '#{pane_id}' | head -1)

  # Right: MONITOR (40% width)
  PANE=$(tmux split-window -h -t "$CLAUDE" -c "$project" -l 40% -PF '#{pane_id}')
  local MONITOR="$PANE"
  run_monitor

  # Below MONITOR: FILES (50% height of right column)
  PANE=$(tmux split-window -v -t "$MONITOR" -c "$project" -l 50% -PF '#{pane_id}')
  run_filetree "$project"

  # Below CLAUDE: LAZYGIT (35% height of left column)
  PANE=$(tmux split-window -v -t "$CLAUDE" -c "$project" -l 35% -PF '#{pane_id}')
  local LAZYGIT="$PANE"
  run_lazygit "$project"

  # Right of LAZYGIT: SHELL (45% width of bottom)
  PANE=$(tmux split-window -h -t "$LAZYGIT" -c "$project" -l 45% -PF '#{pane_id}')
  run_shell "$project"

  # Launch Claude in the first pane
  PANE="$CLAUDE"
  run_claude "$project"

  apply_theme "$theme" "$session"
  tmux select-pane -t "$CLAUDE"
}

# ── Layout: claude (7-pane, Claude-focused) ──────────────────────────────────
# ┌──────────────┬──────────┐
# │              │  MONITOR │
# │   CLAUDE     ├──────────┤
# │              │  FILES   │
# │              ├──────────┤
# │              │  REMOTE  │
# ├──────────────┼─────┬────┤
# │   LAZYGIT    │SHELL│LOG │
# └──────────────┴─────┴────┘
layout_claude() {
  local session="$1" project="$2" theme="$3"

  tmux new-session -d -s "$session" -c "$project" -x 220 -y 60
  local CLAUDE
  CLAUDE=$(tmux list-panes -t "$session" -F '#{pane_id}' | head -1)

  # Right column: MONITOR (40% width)
  PANE=$(tmux split-window -h -t "$CLAUDE" -c "$project" -l 40% -PF '#{pane_id}')
  local MONITOR="$PANE"
  run_monitor

  # Below MONITOR: FILES (60% of right column)
  PANE=$(tmux split-window -v -t "$MONITOR" -c "$project" -l 60% -PF '#{pane_id}')
  local FILES="$PANE"
  run_filetree "$project"

  # Below FILES: REMOTE (50% of remaining right)
  PANE=$(tmux split-window -v -t "$FILES" -c "$project" -l 50% -PF '#{pane_id}')
  run_remote "$project"

  # Below CLAUDE: LAZYGIT (30% height)
  PANE=$(tmux split-window -v -t "$CLAUDE" -c "$project" -l 30% -PF '#{pane_id}')
  local LAZYGIT="$PANE"
  run_lazygit "$project"

  # Right of LAZYGIT: SHELL (60% of bottom)
  PANE=$(tmux split-window -h -t "$LAZYGIT" -c "$project" -l 60% -PF '#{pane_id}')
  local SHELL_PANE="$PANE"
  run_shell "$project"

  # Right of SHELL: LOG (40% of remaining bottom)
  PANE=$(tmux split-window -h -t "$SHELL_PANE" -c "$project" -l 40% -PF '#{pane_id}')
  run_logs "$project"

  # Claude in first pane
  PANE="$CLAUDE"
  run_claude "$project"

  apply_theme "$theme" "$session"
  tmux select-pane -t "$CLAUDE"
}

# ── Layout: monitor (4-pane, server monitoring) ──────────────────────────────
# ┌──────────────┬──────────┐
# │   MONITOR    │  SHELL   │
# ├──────────────┼──────────┤
# │   LAZYGIT    │  LOGS    │
# └──────────────┴──────────┘
layout_monitor() {
  local session="$1" project="$2" theme="$3"

  tmux new-session -d -s "$session" -c "$project" -x 220 -y 60
  local FIRST
  FIRST=$(tmux list-panes -t "$session" -F '#{pane_id}' | head -1)

  # Right: SHELL (45% width)
  PANE=$(tmux split-window -h -t "$FIRST" -c "$project" -l 45% -PF '#{pane_id}')
  local SHELL_PANE="$PANE"
  run_shell "$project"

  # Below FIRST: LAZYGIT (50% height)
  PANE=$(tmux split-window -v -t "$FIRST" -c "$project" -l 50% -PF '#{pane_id}')
  run_lazygit "$project"

  # Below SHELL: LOGS (50% height)
  PANE=$(tmux split-window -v -t "$SHELL_PANE" -c "$project" -l 50% -PF '#{pane_id}')
  run_logs "$project"

  # MONITOR in first pane
  PANE="$FIRST"
  run_monitor

  apply_theme "$theme" "$session"
  tmux select-pane -t "$FIRST"
}

# ── Layouts: ahub and ahub-compact (agent-hub first, #331) ──────────────────
# ahub (200x56 or larger; smaller terminals get ahub-compact):
# ┌──────────┬──────────────────┐
# │          │  PEER A (60%)    │
# │  AHUB    ├──────────────────┤
# │  ≥80 col │  PEER B (40%)    │
# └──────────┴──────────────────┘   window "verify": project shell
# ahub-compact: one full-size window each: hub, peer-a, peer-b, verify.
# Peer ids and launch args come from `dot open` (DOT_AHUB_PEER_{A,B}_{ID,ARGS});
# a slot starts its peer only on Enter (ahub-pane.sh).

# Prints "<cols> <rows> <window rows>": the terminal's size (DOT_COLS/DOT_ROWS
# from the launcher, else 220x60) and the rows left for the window under the
# status line. A detached session does not subtract the status line until a
# client attaches, so it is created at the window size the client will see.
_ahub_size() {
  local cols="${DOT_COLS:-}" rows="${DOT_ROWS:-}" status
  [[ "$cols" =~ ^[0-9]+$ ]] && (( cols > 0 )) || cols=220
  [[ "$rows" =~ ^[0-9]+$ ]] && (( rows > 0 )) || rows=60
  status=$(tmux show-options -gv status 2>/dev/null)
  case "$status" in
    off) status=0 ;;
    [2-5]) ;;
    *) status=1 ;;
  esac
  echo "$cols $rows $(( rows - status ))"
}

# Marks the session and its slots so `dot hub` can find them.
_ahub_own() {
  local session="$1" project="$2"
  tmux set-option -t "$session" @dot_root "$project" >/dev/null
}

_ahub_slot() {
  local slot="$1" project="$2" title="$3"
  shift 3
  label_pane "$PANE" "$title"
  tmux set-option -p -t "$PANE" @dot_slot "$slot" >/dev/null
  local cmd
  cmd="bash $(printf '%q' "$SCRIPT_DIR/ahub-pane.sh")"
  for arg in "$@"; do cmd+=" $(printf '%q' "$arg")"; done
  tmux send-keys -t "$PANE" "$cmd" Enter
}

run_ahub_hub() { _ahub_slot hub "$1" "AHUB" hub "$1"; }

# run_ahub_peer <project> A|B
run_ahub_peer() {
  local project="$1" slot="$2" id_var args_var lower
  lower=$(printf '%s' "$slot" | tr 'AB' 'ab')
  id_var="DOT_AHUB_PEER_${slot}_ID"
  args_var="DOT_AHUB_PEER_${slot}_ARGS"
  local id="${!id_var:-}" args="${!args_var:-}"
  if [ -z "$id" ]; then
    label_pane "$PANE" "PEER $slot"
    tmux set-option -p -t "$PANE" @dot_slot "peer-$lower" >/dev/null
    tmux send-keys -t "$PANE" "echo 'No peer for slot $slot. Start one with: ahub --project <root> <kind>'" Enter
    return
  fi
  local -a argv
  read -r -a argv <<<"$args"
  _ahub_slot "peer-$lower" "$project" "PEER $slot: $id" peer "$project" "$id" ${argv[@]+"${argv[@]}"}
}

_ahub_verify() {
  local session="$1" project="$2"
  PANE=$(tmux new-window -d -t "$session:" -n verify -c "$project" -PF '#{pane_id}')
  label_pane "$PANE" "VERIFY"
  tmux set-option -p -t "$PANE" @dot_slot verify >/dev/null
}

layout_ahub() {
  local session="$1" project="$2" theme="$3" cols rows wrows
  read -r cols rows wrows < <(_ahub_size)
  if (( cols < 200 || rows < 56 )); then
    echo "Terminal is ${cols}x${rows}, below 200x56: using ahub-compact."
    layout_ahub_compact "$session" "$project" "$theme"
    return
  fi
  tmux new-session -d -s "$session" -n work -c "$project" -x "$cols" -y "$wrows"
  _ahub_own "$session" "$project"
  local HUB PEER_A hub_cols=$(( cols * 40 / 100 ))
  (( hub_cols < 80 )) && hub_cols=80
  HUB=$(tmux list-panes -t "$session:work" -F '#{pane_id}' | head -1)

  PANE=$(tmux split-window -h -t "$HUB" -c "$project" -l $(( cols - hub_cols - 1 )) -PF '#{pane_id}')
  PEER_A="$PANE"
  run_ahub_peer "$project" A
  PANE=$(tmux split-window -v -t "$PEER_A" -c "$project" -l 40% -PF '#{pane_id}')
  run_ahub_peer "$project" B
  PANE="$HUB"
  run_ahub_hub "$project"
  _ahub_verify "$session" "$project"

  apply_theme "$theme" "$session"
  tmux select-window -t "$session:work"
  tmux select-pane -t "$HUB"
}

layout_ahub_compact() {
  local session="$1" project="$2" theme="$3" cols rows wrows
  read -r cols rows wrows < <(_ahub_size)
  tmux new-session -d -s "$session" -n hub -c "$project" -x "$cols" -y "$wrows"
  _ahub_own "$session" "$project"
  PANE=$(tmux list-panes -t "$session:hub" -F '#{pane_id}' | head -1)
  run_ahub_hub "$project"
  PANE=$(tmux new-window -d -t "$session:" -n peer-a -c "$project" -PF '#{pane_id}')
  run_ahub_peer "$project" A
  PANE=$(tmux new-window -d -t "$session:" -n peer-b -c "$project" -PF '#{pane_id}')
  run_ahub_peer "$project" B
  _ahub_verify "$session" "$project"

  apply_theme "$theme" "$session"
  tmux select-window -t "$session:hub"
}

# ── Layout dispatcher ────────────────────────────────────────────────────────

load_layout() {
  local layout="$1" session="$2" project="$3" theme="${4:-default}"

  case "$layout" in
    dev)     layout_dev "$session" "$project" "$theme" ;;
    claude)  layout_claude "$session" "$project" "$theme" ;;
    monitor) layout_monitor "$session" "$project" "$theme" ;;
    ahub)    layout_ahub "$session" "$project" "$theme" ;;
    ahub-compact) layout_ahub_compact "$session" "$project" "$theme" ;;
    *)
      echo "Unknown layout: $layout (using dev)"
      layout_dev "$session" "$project" "$theme"
      ;;
  esac
}
