#!/usr/bin/env bash
# launcher.sh — Tmux workspace session launcher for dotfiles
# Usage: launcher.sh <session> <project_dir> <layout> <theme>
set -uo pipefail
# NOTE: no set -e — tmux commands can return non-zero in normal operation

SESSION="${1:?Usage: launcher.sh <session> <project_dir> <layout> <theme>}"
PROJECT="${2:?Missing project directory}"
LAYOUT="${3:-dev}"
THEME="${4:-default}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ── Sanity checks ────────────────────────────────────────────────────────────

if ! command -v tmux &>/dev/null; then
  echo "Error: tmux is not installed. Run 'dot apply' to install it."
  exit 1
fi

if [ ! -d "$PROJECT" ]; then
  echo "Error: project directory does not exist: $PROJECT"
  exit 1
fi

# ── Nested tmux detection ──────────────────────────────────────────────────

if [ -n "${TMUX:-}" ]; then
  echo "Already inside a tmux session."
  echo "Use 'tmux switch-client -t $SESSION' to switch, or detach first (C-a d)."
  exit 0
fi

# ── Stale socket cleanup ────────────────────────────────────────────────────

_cleanup_sockets() {
  local uid
  uid=$(id -u)
  local dirs=("/tmp/tmux-$uid")
  # macOS has /private/tmp alias
  [ -d "/private/tmp/tmux-$uid" ] && dirs+=("/private/tmp/tmux-$uid")

  for socket_dir in "${dirs[@]}"; do
    [ -d "$socket_dir" ] || continue
    for sock in "$socket_dir"/*; do
      [ -e "$sock" ] || continue  # handle empty glob
      [ -S "$sock" ] || continue
      # Only remove truly dead sockets — test with a quick list-sessions
      # Use timeout to avoid hangs on stuck servers
      if command -v timeout &>/dev/null; then
        timeout 2 tmux -S "$sock" list-sessions &>/dev/null && continue
      elif command -v gtimeout &>/dev/null; then
        gtimeout 2 tmux -S "$sock" list-sessions &>/dev/null && continue
      else
        tmux -S "$sock" list-sessions &>/dev/null && continue
      fi
      rm -f "$sock"
    done
  done
}

# ── Session check with timeout ──────────────────────────────────────────────

_tmux_has_session() {
  local name="$1"
  if command -v timeout &>/dev/null; then
    timeout 3 tmux has-session -t "$name" 2>/dev/null
  elif command -v gtimeout &>/dev/null; then
    gtimeout 3 tmux has-session -t "$name" 2>/dev/null
  else
    tmux has-session -t "$name" 2>/dev/null
  fi
}

# ── Main ─────────────────────────────────────────────────────────────────────

if _tmux_has_session "$SESSION"; then
  echo "Resuming session: $SESSION"
  # A live session is never rebuilt: a requested layout applies only to a new one.
  if [ -n "${DOT_LAYOUT_REQUESTED:-}" ]; then
    echo "The existing layout is kept unchanged; --layout $LAYOUT applies only to a new session (dot stop $SESSION first)."
  fi
  exec tmux attach-session -t "$SESSION"
fi

# The ahub layouts size the new session to this terminal.
if size=$(stty size </dev/tty 2>/dev/null); then
  read -r DOT_ROWS DOT_COLS <<<"$size"
  export DOT_ROWS DOT_COLS
fi

# Session doesn't exist — create it
echo "Creating workspace: $SESSION (layout=$LAYOUT, theme=$THEME)"

_cleanup_sockets

# Source layouts (which sources tools.sh and themes.sh)
source "$SCRIPT_DIR/layouts.sh"

# Create the layout
if ! load_layout "$LAYOUT" "$SESSION" "$PROJECT" "$THEME"; then
  echo "Warning: layout had errors. Retrying after cleanup..."
  # Kill the partial session before retrying
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  _cleanup_sockets
  sleep 1
  if ! load_layout "$LAYOUT" "$SESSION" "$PROJECT" "$THEME"; then
    echo "Error: workspace creation failed."
    echo "Try: tmux kill-server && dot open $SESSION"
    exit 1
  fi
fi

# Attach to the session
exec tmux attach-session -t "$SESSION"
