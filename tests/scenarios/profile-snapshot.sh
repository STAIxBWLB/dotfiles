#!/usr/bin/env bash
# Scenario: dot profile backup/list/restore round-trip in an isolated home.
set -euo pipefail
source "$(dirname "$0")/../assert.sh"

echo "=== Scenario: profile snapshot round-trip ==="

tmphome=$(mktemp -d)
backup_root=$(mktemp -d)
trap 'rm -rf "$tmphome" "$backup_root"' EXIT

mkdir -p "$tmphome/.config/dotfiles" "$tmphome/.ssh"
cat > "$tmphome/.config/dotfiles/config.yaml" <<YAML
name: Scenario Test
email: scenario@example.com
profile: full
modules:
  macapps:
    enabled: true
    casks: [raycast, obsidian]
    casks_extra: [maccy]
    backup_apps: [raycast, obsidian]
    backup_root: $backup_root
YAML
echo 'AGE-SECRET-KEY-SCENARIO' > "$tmphome/.ssh/age_key"
echo 'age1scenario'            > "$tmphome/.ssh/age_key.pub"
chmod 600 "$tmphome/.ssh/age_key"

# 1. backup (no secrets)
dot --home "$tmphome" profile backup --to "$backup_root" --tag "first" --yes
assert_dir_exists "$backup_root/profiles/$(hostname -s)" "host root created"
assert_file_exists "$backup_root/profiles/$(hostname -s)/latest.txt" "latest pointer written"

# 2. backup (with secrets)
dot --home "$tmphome" profile backup --to "$backup_root" --tag "second" --include-age-key --yes

# 3. list shows 2 snapshots
count=$(dot --home "$tmphome" profile list --from "$backup_root" | grep -c "2026\|20[0-9][0-9]-" || true)
if [[ "$count" -ge 2 ]]; then
  PASS=$((PASS + 1)); echo "  ✓ profile list shows ≥2 snapshots"
else
  FAIL=$((FAIL + 1)); ERRORS+=("FAIL: profile list count = $count"); echo "  ✗ profile list count = $count"
fi

# 4. mutate state + restore
echo 'name: mutated' > "$tmphome/.config/dotfiles/config.yaml"
echo 'MUTATED' > "$tmphome/.ssh/age_key"

dot --home "$tmphome" profile restore --from "$backup_root" --include-age-key --yes >/dev/null
if grep -q "Scenario Test" "$tmphome/.config/dotfiles/config.yaml"; then
  PASS=$((PASS + 1)); echo "  ✓ restore recovered state"
else
  FAIL=$((FAIL + 1)); ERRORS+=("FAIL: state not restored"); echo "  ✗ state not restored"
fi
if grep -q "AGE-SECRET-KEY-SCENARIO" "$tmphome/.ssh/age_key"; then
  PASS=$((PASS + 1)); echo "  ✓ restore recovered age_key"
else
  FAIL=$((FAIL + 1)); ERRORS+=("FAIL: age_key not restored"); echo "  ✗ age_key not restored"
fi

# 5. prune down to 1
dot --home "$tmphome" profile prune --from "$backup_root" --keep 1 --yes >/dev/null
# D-03/SC2010: count with a find predicate rather than parsing ls through grep,
# the register tests/scenarios/sync.sh already uses. -mindepth/-maxdepth 1 keeps
# this to the immediate children; ! -name excludes the latest.txt pointer.
remaining=$(find "$backup_root/profiles/$(hostname -s)" -mindepth 1 -maxdepth 1 ! -name latest.txt | wc -l | tr -d ' ')
if [[ "$remaining" -eq 1 ]]; then
  PASS=$((PASS + 1)); echo "  ✓ prune kept 1 snapshot"
else
  FAIL=$((FAIL + 1)); ERRORS+=("FAIL: prune remaining=$remaining"); echo "  ✗ prune remaining=$remaining"
fi

report
