#!/usr/bin/env bash
set -euo pipefail

REPO="STAIxBWLB/dotfiles"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"

# --- Helpers ---

if [[ -z "${NO_COLOR:-}" ]] && [[ -t 1 ]]; then
  _green='\033[0;32m' _yellow='\033[0;33m' _red='\033[0;31m' _reset='\033[0m'
else
  _green='' _yellow='' _red='' _reset=''
fi

info() { printf "${_green}[+]${_reset} %s\n" "$*"; }
warn() { printf "${_yellow}[!]${_reset} %s\n" "$*"; }
err()  { printf "${_red}[x]${_reset} %s\n" "$*" >&2; }

# --- Step 1: Detect OS and arch ---

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in
  x86_64)        ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) err "Unsupported architecture: $ARCH"; exit 1 ;;
esac

info "Detected ${OS}/${ARCH}"

# --- Step 2: Ensure Homebrew ---
# macOS: Homebrew's installer also handles Xcode Command Line Tools automatically.
# Linux: Linuxbrew provides consistent package management for dot apply.

if [[ "$OS" == "darwin" ]]; then
  if [[ "$ARCH" == "arm64" ]]; then
    BREW_PREFIX="/opt/homebrew"
  else
    BREW_PREFIX="/usr/local"
  fi
elif [[ "$OS" == "linux" ]]; then
  BREW_PREFIX="/home/linuxbrew/.linuxbrew"
fi

if command -v brew &>/dev/null; then
  info "Homebrew: $(brew --version 2>/dev/null | head -1)"
elif [[ -n "${BREW_PREFIX:-}" ]] && [[ -x "$BREW_PREFIX/bin/brew" ]]; then
  eval "$("$BREW_PREFIX/bin/brew" shellenv)"
  info "Homebrew: $(brew --version 2>/dev/null | head -1)"
elif [[ -n "${BREW_PREFIX:-}" ]]; then
  if [[ "$OS" == "darwin" ]]; then
    info "Installing Homebrew (includes Xcode Command Line Tools)..."
  else
    info "Installing Homebrew (Linuxbrew)..."
    if command -v apt-get &>/dev/null; then
      info "Installing Linuxbrew prerequisites..."
      sudo apt-get update -qq
      sudo apt-get install -y build-essential procps curl file git
    fi
  fi
  NONINTERACTIVE=1 /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
  if [[ -x "$BREW_PREFIX/bin/brew" ]]; then
    eval "$("$BREW_PREFIX/bin/brew" shellenv)"
  fi
  info "Homebrew installed"
fi

# --- Step 3: Download dot binary ---

# Fetch latest release tag with fallback chain:
#   1. GitHub API with GITHUB_TOKEN (if set)
#   2. GitHub API without auth
#   3. GitHub redirect URL (no API needed)
# Uses -sL (no -f) to avoid pipefail interaction, separates curl from grep.
fetch_latest_tag() {
  local body="" tag=""

  # Try with token first (if set)
  if [[ -n "${GITHUB_TOKEN:-}" ]]; then
    body=$(curl -sL --no-netrc -H "Authorization: token $GITHUB_TOKEN" \
      "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null) || true
    tag=$(echo "$body" | grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/') || true
    if [[ -n "$tag" ]]; then echo "$tag"; return; fi
  fi

  # Fallback: API without auth
  body=$(curl -sL --no-netrc \
    "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null) || true
  tag=$(echo "$body" | grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/') || true
  if [[ -n "$tag" ]]; then echo "$tag"; return; fi

  # Fallback: parse redirect URL (no API, no rate limit)
  tag=$(curl -sIL --no-netrc -o /dev/null -w '%{url_effective}' \
    "https://github.com/${REPO}/releases/latest" 2>/dev/null \
    | sed -E 's|.*/tag/||') || true
  echo "$tag"
}

LATEST=$(fetch_latest_tag)

if [[ -z "$LATEST" ]]; then
  err "Failed to fetch latest release from GitHub"
  exit 1
fi

VERSION="${LATEST#v}"

# Download, checksum, and activation helpers. Release bytes are never passed
# to tar until checksums.txt has selected and verified the exact asset.
select_checksum_tool() {
  if command -v sha256sum >/dev/null 2>&1; then
    CHECKSUM_TOOL="sha256sum"
    return 0
  fi
  if command -v shasum >/dev/null 2>&1; then
    CHECKSUM_TOOL="shasum"
    return 0
  fi
  err "Cannot verify release checksum: install sha256sum or shasum -a 256; active binary remains unchanged"
  return 1
}

select_checksum_line() {
  local checksums="$1" asset="$2" line="" match="" count=0 digest="" filename="" extra=""
  while IFS= read -r line || [[ -n "$line" ]]; do
    if [[ "$line" == *"$asset" ]]; then
      digest="" filename="" extra=""
      read -r digest filename extra <<< "$line"
      filename="${filename#\*}"
      if [[ "$filename" == "$asset" && -z "$extra" && ${#digest} -eq 64 && "$digest" != *[!0123456789abcdefABCDEF]* ]]; then
        match="$digest"
        count=$((count + 1))
      else
        err "Malformed checksum entry for ${asset}; active binary remains unchanged. Download the release again."
        return 1
      fi
    fi
  done < "$checksums"
  if [[ "$count" -ne 1 ]]; then
    err "Checksum entry for ${asset} is missing or duplicated; active binary remains unchanged. Download the release again."
    return 1
  fi
  printf '%s\n' "$match"
}

verify_checksum() {
  local stage="$1" asset="$2" checksum="$3"
  printf '%s  %s\n' "$checksum" "$asset" > "$stage/selected-checksum"
  if [[ "$CHECKSUM_TOOL" == "sha256sum" ]]; then
    (cd "$stage" && sha256sum -c selected-checksum >/dev/null 2>&1)
  else
    (cd "$stage" && shasum -a 256 -c selected-checksum >/dev/null 2>&1)
  fi
}

download_file() {
  local url="$1" dest="$2"
  if ! curl -fSL --no-netrc --proto '=https' --tlsv1.2 --retry 3 --retry-delay 1 -o "$dest" "$url" 2>/dev/null; then
    err "Download failed for ${url}; active binary remains unchanged. Check your network and retry."
    return 1
  fi
}

validate_binary() {
  local candidate="$1"
  [[ -f "$candidate" && ! -L "$candidate" ]] || return 1
  chmod 0755 "$candidate" || return 1
  "$candidate" --version >/dev/null 2>&1
}

restore_active_binary() {
  local dest="$1" binary_rollback="$2" link_rollback="$3" had_compatibility="$4"
  if [[ -e "$binary_rollback" ]]; then
    rm -f "$dest/dot"
    mv "$binary_rollback" "$dest/dot" || return 1
  else
    rm -f "$dest/dot"
  fi
  if [[ -e "$link_rollback" || -L "$link_rollback" ]]; then
    rm -f "$dest/dotfiles"
    mv "$link_rollback" "$dest/dotfiles" || return 1
  elif [[ "$had_compatibility" == "0" ]]; then
    rm -f "$dest/dotfiles"
  fi
}

ensure_compatibility_symlink() {
  local dest="$1" had_compatibility=0
  local link_rollback="$dest/.dotfiles.rollback.$$"

  if [[ -L "$dest/dotfiles" ]] && [[ "$(readlink "$dest/dotfiles")" == "dot" ]]; then
    return 0
  fi
  if [[ -e "$dest/dotfiles" || -L "$dest/dotfiles" ]]; then
    had_compatibility=1
    if ! mv "$dest/dotfiles" "$link_rollback"; then
      err "Could not stage compatibility symlink; active binary remains unchanged. Check permissions and retry."
      return 1
    fi
  fi
  if ! ln -s dot "$dest/dotfiles"; then
    rm -f "$dest/dotfiles"
    if [[ "$had_compatibility" == "1" ]] && ! mv "$link_rollback" "$dest/dotfiles"; then
      err "Could not restore compatibility command; recover ${link_rollback} manually."
    fi
    err "Could not create compatibility symlink; active binary remains unchanged. Check permissions and retry."
    return 1
  fi
  rm -f "$link_rollback"
}

install_binary() {
  local candidate="$1" dest="$2"
  local binary_rollback="$dest/.dot.rollback.$$" link_rollback="$dest/.dotfiles.rollback.$$"
  local had_binary=0 had_compatibility=0

  if ! validate_binary "$candidate"; then
    err "Downloaded ${ASSET_NAME} is not a valid dot binary; active binary remains unchanged. Download the release again."
    return 1
  fi
  [[ -e "$dest/dot" ]] && had_binary=1
  [[ -e "$dest/dotfiles" || -L "$dest/dotfiles" ]] && had_compatibility=1
  INSTALL_ACTIVE_DEST="$dest"
  INSTALL_BINARY_ROLLBACK="$binary_rollback"
  INSTALL_LINK_ROLLBACK="$link_rollback"
  INSTALL_HAD_LINK="$had_compatibility"

  if [[ "$had_binary" == "1" ]] && ! mv "$dest/dot" "$binary_rollback"; then
    err "Could not stage active binary replacement; active binary remains unchanged. Check permissions and retry."
    return 1
  fi
  if ! mv "$candidate" "$dest/dot" || ! validate_binary "$dest/dot"; then
    restore_active_binary "$dest" "$binary_rollback" "$link_rollback" "$had_compatibility" || err "Rollback failed; recover ${binary_rollback} manually."
    err "Could not promote verified ${ASSET_NAME}; active binary remains unchanged. Check permissions and retry."
    return 1
  fi
  if [[ "$had_compatibility" == "1" ]] && ! mv "$dest/dotfiles" "$link_rollback"; then
    restore_active_binary "$dest" "$binary_rollback" "$link_rollback" "$had_compatibility" || err "Rollback failed; recover ${binary_rollback} manually."
    err "Could not update compatibility symlink; active binary remains unchanged. Check permissions and retry."
    return 1
  fi
  if ! ln -s dot "$dest/dotfiles"; then
    restore_active_binary "$dest" "$binary_rollback" "$link_rollback" "$had_compatibility" || err "Rollback failed; recover ${binary_rollback} manually."
    err "Could not create compatibility symlink; active binary remains unchanged. Check permissions and retry."
    return 1
  fi
  rm -f "$binary_rollback" "$link_rollback"
  INSTALL_ACTIVE_DEST=""
  INSTALL_BINARY_ROLLBACK=""
  INSTALL_LINK_ROLLBACK=""
}

INSTALL_PARENT=$(dirname "$INSTALL_DIR")
mkdir -p "$INSTALL_PARENT" "$INSTALL_DIR"
ASSET_NAME="dot_${VERSION}_${OS}_${ARCH}.tar.gz"
ASSET_URL="https://github.com/${REPO}/releases/download/${LATEST}/${ASSET_NAME}"
CHECKSUMS_URL="https://github.com/${REPO}/releases/download/${LATEST}/checksums.txt"

# Skip download if already at latest version.
if [[ -x "$INSTALL_DIR/dot" ]]; then
  CURRENT=$("$INSTALL_DIR/dot" --version 2>/dev/null || echo "")
else
  CURRENT=""
fi

if [[ "$CURRENT" == *"$VERSION"* ]]; then
  info "dot v${VERSION} already installed, skipping download"
  ensure_compatibility_symlink "$INSTALL_DIR" || exit 1
else
  if [[ -n "$CURRENT" ]]; then
    info "Upgrading dot to v${VERSION}..."
  else
    info "Installing dot v${VERSION}..."
  fi
  STAGE_DIR=$(mktemp -d "$INSTALL_PARENT/.dot-install.XXXXXX")
  cleanup_stage() { rm -rf "$STAGE_DIR"; }
  INSTALL_ACTIVE_DEST=""
  INSTALL_BINARY_ROLLBACK=""
  INSTALL_LINK_ROLLBACK=""
  INSTALL_HAD_LINK=0
  abort_install() {
    if [[ -n "$INSTALL_ACTIVE_DEST" ]]; then
      restore_active_binary "$INSTALL_ACTIVE_DEST" "$INSTALL_BINARY_ROLLBACK" "$INSTALL_LINK_ROLLBACK" "$INSTALL_HAD_LINK" || err "Rollback failed; recover ${INSTALL_BINARY_ROLLBACK} manually."
    fi
    err "Installation interrupted; active binary remains unchanged. Retry the installation."
    cleanup_stage
    trap - EXIT HUP INT TERM
    exit 1
  }
  trap cleanup_stage EXIT
  trap abort_install HUP INT TERM

  if ! select_checksum_tool || ! download_file "$ASSET_URL" "$STAGE_DIR/$ASSET_NAME" || ! download_file "$CHECKSUMS_URL" "$STAGE_DIR/checksums.txt"; then
    exit 1
  fi
  EXPECTED_SUM=$(select_checksum_line "$STAGE_DIR/checksums.txt" "$ASSET_NAME") || exit 1
  if ! verify_checksum "$STAGE_DIR" "$ASSET_NAME" "$EXPECTED_SUM"; then
    err "Checksum verification failed for ${ASSET_NAME}; active binary remains unchanged. Download the release again."
    exit 1
  fi
  EXTRACT_DIR="$STAGE_DIR/extract"
  mkdir -p "$EXTRACT_DIR"
  if ! tar xzf "$STAGE_DIR/$ASSET_NAME" -C "$EXTRACT_DIR" dot; then
    err "Could not extract verified ${ASSET_NAME}; active binary remains unchanged. Download the release again."
    exit 1
  fi
  if ! install_binary "$EXTRACT_DIR/dot" "$INSTALL_DIR"; then
    exit 1
  fi
  trap - EXIT HUP INT TERM
  cleanup_stage
fi

# --- Step 4: Ensure PATH ---

ensure_path() {
  local target_dir="$1"

  # Already in PATH
  case ":$PATH:" in
    *":$target_dir:"*) return 0 ;;
  esac

  # Detect shell RC file
  local shell_name rc_file
  shell_name="$(basename "${SHELL:-/bin/zsh}")"
  case "$shell_name" in
    zsh)  rc_file="$HOME/.zshrc" ;;
    bash) rc_file="$HOME/.bashrc" ;;
    *)    rc_file="$HOME/.profile" ;;
  esac

  local path_line="export PATH=\"$target_dir:\$PATH\""

  if [[ -f "$rc_file" ]] && grep -qF "$target_dir" "$rc_file" 2>/dev/null; then
    export PATH="$target_dir:$PATH"
    return 0
  fi

  info "Adding $target_dir to PATH in $rc_file"
  {
    echo ""
    echo "# Added by dot installer"
    echo "$path_line"
  } >> "$rc_file"

  export PATH="$target_dir:$PATH"
}

ensure_path "$INSTALL_DIR"

# --- Step 5: Verify and show next steps ---

if command -v dot &>/dev/null; then
  info "dot $(dot --version 2>/dev/null) is ready"
elif [[ -x "$INSTALL_DIR/dot" ]]; then
  info "dot $("$INSTALL_DIR/dot" --version 2>/dev/null) installed at $INSTALL_DIR/dot"
  warn "Open a new terminal or run: export PATH=\"$INSTALL_DIR:\$PATH\""
else
  err "Installation failed -- dot binary not found"
  exit 1
fi

echo ""
echo "=== Next steps ==="
echo ""
echo "  dot init              # Setup wizard"
echo "  dot apply             # Install packages & configure environment"
echo "  dot secrets restore   # Decrypt SSH keys & secrets (optional)"
echo "  dot sync setup        # Configure server sync (optional)"
echo ""
