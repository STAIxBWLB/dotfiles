#!/usr/bin/env bash
# run-profile.sh — Profile integration test
set -euo pipefail
source "$(dirname "$0")/assert.sh"

PROFILE="${1:?Usage: run-profile.sh <profile>}"

echo "=== Profile integration test: $PROFILE ==="

# Dry-run should succeed
echo ""
echo "--- dry-run ---"
dot apply --profile "$PROFILE" --yes --dry-run
assert_exit_code 0 dot apply --profile "$PROFILE" --yes --dry-run

# Init + Apply
echo ""
echo "--- init ---"
dot init --profile "$PROFILE" --yes

echo ""
echo "--- apply ---"
dot apply --profile "$PROFILE" --yes

# Check should pass after apply
echo ""
echo "--- diff --summary ---"
dot diff --summary --profile "$PROFILE"

# Profile-specific assertions
case "$PROFILE" in
  minimal)
    assert_dir_exists "$HOME/.config/dotfiles" "Config directory exists"
    assert_file_exists "$HOME/.config/dotfiles/config.yaml" "Config file exists"
    ;;
  full)
    assert_dir_exists "$HOME/.config/dotfiles" "Config directory exists"
    assert_file_exists "$HOME/.config/dotfiles/config.yaml" "Config file exists"
    ;;
  server)
    assert_dir_exists "$HOME/.config/dotfiles" "Config directory exists"
    assert_file_exists "$HOME/.config/dotfiles/config.yaml" "Config file exists"
    ;;
esac

report
