#!/usr/bin/env bash
# Scenario: init → apply → diff --summary
set -euo pipefail
source "$(dirname "$0")/../assert.sh"

echo "=== Scenario: init-and-apply ==="

echo ""
echo "--- init ---"
dot init --yes
assert_exit_code 0 dot init --yes

echo ""
echo "--- apply ---"
dot apply --yes
assert_exit_code 0 dot apply --yes

echo ""
echo "--- diff --summary ---"
dot diff --summary
assert_exit_code 0 dot diff --summary

assert_dir_exists "$HOME/.config/dotfiles" "Config directory created"
assert_file_exists "$HOME/.config/dotfiles/config.yaml" "Config file created"

report
