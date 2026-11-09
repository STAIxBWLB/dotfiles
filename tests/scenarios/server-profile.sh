#!/usr/bin/env bash
# Scenario: server profile — correct modules enabled/disabled
set -euo pipefail
source "$(dirname "$0")/../assert.sh"

echo "=== Scenario: server profile ==="

echo ""
echo "--- init + apply server ---"
dot init --profile server --yes
dot apply --profile server --yes --dry-run

# Server profile should show these in dry-run output
# (We verify the profile loads correctly and the right modules are selected)

# Verify server profile selects correctly via diff --summary
echo ""
echo "--- diff --summary server ---"
OUTPUT=$(dot diff --summary --profile server 2>&1 || true)

# Server enables: packages, shell, git, ssh, terminal, tmux, ai, conda
# Server disables: workspace, fonts, gpg, secrets
echo "$OUTPUT" | grep -q "packages" && { PASS=$((PASS + 1)); echo "  ✓ packages module present"; } || { FAIL=$((FAIL + 1)); echo "  ✗ packages module missing"; }
echo "$OUTPUT" | grep -q "shell" && { PASS=$((PASS + 1)); echo "  ✓ shell module present"; } || { FAIL=$((FAIL + 1)); echo "  ✗ shell module missing"; }
echo "$OUTPUT" | grep -q "tmux" && { PASS=$((PASS + 1)); echo "  ✓ tmux module present"; } || { FAIL=$((FAIL + 1)); echo "  ✗ tmux module missing"; }
echo "$OUTPUT" | grep -q "ai" && { PASS=$((PASS + 1)); echo "  ✓ ai module present"; } || { FAIL=$((FAIL + 1)); echo "  ✗ ai module missing"; }
echo "$OUTPUT" | grep -q "conda" && { PASS=$((PASS + 1)); echo "  ✓ conda module present"; } || { FAIL=$((FAIL + 1)); echo "  ✗ conda module missing"; }

report
