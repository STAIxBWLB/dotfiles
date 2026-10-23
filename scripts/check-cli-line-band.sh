#!/usr/bin/env bash
# Fail the build when a decomposed command file leaves the SPLIT-07 line band.
set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "$script_dir/.." && pwd)

# The band's exemplars are the repo's own already-thin command files:
# internal/cli/sync_json.go (161 lines) and internal/cli/sync_configure.go
# (227). Both bounds are inclusive.
band_min=161
band_max=350

# One entry per decomposed command file, in the order the slices land. The
# list is fixed rather than globbed on purpose: a glob would quietly stop
# covering a file that gets renamed, and would start covering command files
# nobody has decomposed yet, which is a different requirement.
files=(
  "internal/cli/tunnel_cmd.go"
  "internal/cli/secrets.go"
  "internal/cli/apps_cmd.go"
  "internal/cli/ai_cmd.go"
  "internal/cli/peer_cmd.go"
  "internal/cli/sync_cmd.go"
  "internal/cli/watchdog_cmd.go"
  "internal/cli/watchdog_setup.go"
  "internal/cli/watchdog_warp.go"
  "internal/cli/watchdog_monit.go"
  "internal/cli/watchdog_beszel.go"
  "internal/cli/admit_cmd.go"
  "internal/cli/admit_run.go"
)

if [[ ${#files[@]} -eq 0 ]]; then
  echo "check-cli-line-band: the file list is empty, so this check would report success without measuring anything" >&2
  exit 1
fi

findings=0
measured=0

# Every listed file is measured before the exit decision: a run that stopped at
# the first finding would hide the second one until the next push.
for file in "${files[@]}"; do
  path="$repo_root/$file"
  if [[ ! -f "$path" ]]; then
    echo "check-cli-line-band: $file is listed but does not exist" >&2
    findings=$((findings + 1))
    continue
  fi
  if [[ ! -r "$path" ]]; then
    echo "check-cli-line-band: $file is listed but is not readable" >&2
    findings=$((findings + 1))
    continue
  fi
  lines=$(wc -l <"$path" | tr -d '[:space:]')
  if [[ "$lines" -eq 0 ]]; then
    echo "check-cli-line-band: $file is empty; a file with no lines cannot satisfy the band" >&2
    findings=$((findings + 1))
    continue
  fi
  measured=$((measured + 1))
  if [[ "$lines" -lt "$band_min" ]]; then
    echo "check-cli-line-band: $file is $lines lines, below the $band_min-$band_max band" >&2
    findings=$((findings + 1))
  elif [[ "$lines" -gt "$band_max" ]]; then
    echo "check-cli-line-band: $file is $lines lines, above the $band_min-$band_max band" >&2
    findings=$((findings + 1))
  fi
done

if [[ "$findings" -gt 0 ]]; then
  echo "check-cli-line-band: $findings finding(s) across ${#files[@]} listed file(s)" >&2
  exit 1
fi

echo "check-cli-line-band: $measured file(s) measured, all within $band_min-$band_max lines"
