#!/usr/bin/env bash
# The `policy` CI check (ci.yml): where GitHub Actions may use macOS and
# secrets. See docs/dev/development.md (CI/CD) and docs/dev/signing.md.
#
#   scripts/workflow-policy.sh [workflows dir]    # default .github/workflows
#
# Rules:
#   1. A macOS runner, an Xcode image or xcodebuild may appear only in
#      - ios.yml (the app's simulator unit tests), or
#      - release.yml, inside a job that declares `environment: release`
#        (the sign-macos job; that environment needs the maintainer's
#        approval and only deploys v* tags).
#      Anywhere else, including release.yml's other jobs and its top level,
#      it fails.
#   2. ios.yml reads no secret at all (not even GITHUB_TOKEN) and asks for
#      no id-token: archives, signing and TestFlight stay on Xcode Cloud.
#   3. In release.yml a secret other than GITHUB_TOKEN may be read only
#      inside a job that declares `environment: release`.
#
# Jobs are found by indentation: two spaces under the top-level `jobs:`,
# their keys at four (the style every workflow here uses).
# Tests: bash scripts/workflow-policy_test.sh
set -euo pipefail

dir="${1:-.github/workflows}"
[[ -d "$dir" ]] || { echo "workflow-policy: no directory $dir" >&2; exit 2; }

shopt -s nullglob
files=("$dir"/*.yml "$dir"/*.yaml)
(( ${#files[@]} )) || { echo "workflow-policy: no workflows in $dir" >&2; exit 2; }

fail=0
for f in "${files[@]}"; do
  base="$(basename "$f")"
  # Two passes over the file: the first finds the jobs that declare
  # `environment: release`, the second checks every line.
  if ! awk -v base="$base" -v file="$f" -v sq="'" '
    function job_of(line) {
      if (line ~ /^jobs:/) { injobs = 1; job = ""; return }
      if (line ~ /^[^ #]/) { injobs = 0; job = ""; return }
      if (injobs && match(line, /^  [A-Za-z0-9_-]+:/)) {
        job = substr(line, 3, RLENGTH - 3); envpending = 0
      }
    }
    BEGIN {
      q = "[\"" sq "]?"   # an optional quote around the value
      env_inline = "^    environment:[ \t]*" q "release" q "[ \t]*(#.*)?$"
      env_block = "^    environment:[ \t]*(#.*)?$"
      env_name = "^      name:[ \t]*" q "release" q "[ \t]*(#.*)?$"
    }
    FNR == 1 { pass++; injobs = 0; job = "" }
    {
      job_of($0)
      if (pass == 1) {
        if (job == "") next
        if ($0 ~ env_inline) envjob[job] = 1
        else if ($0 ~ env_block) envpending = 1
        else if (envpending && $0 ~ env_name) { envjob[job] = 1; envpending = 0 }
        else if ($0 ~ /^    [^ ]/) envpending = 0
        next
      }
      low = tolower($0)
      gated = (base == "release.yml" && job != "" && (job in envjob))
      if (low ~ /macos-|xcode-[0-9]|xcodebuild|runs-on:.*macos/ && base != "ios.yml" && !gated) {
        printf "::error file=%s,line=%d::macOS runners and Xcode belong in ios.yml, or in a release.yml job with environment: release (docs/dev/development.md, CI/CD)\n", file, FNR
        printf "%s:%d: %s\n", file, FNR, $0
        bad = 1
      }
      if (base == "ios.yml" && $0 ~ /secrets\.|GITHUB_TOKEN|id-token/) {
        printf "::error file=%s,line=%d::ios.yml must not use secrets: signing and TestFlight stay on Xcode Cloud\n", file, FNR
        printf "%s:%d: %s\n", file, FNR, $0
        bad = 1
      }
      if (base == "release.yml" && !gated) {
        rest = $0
        gsub(/secrets\.GITHUB_TOKEN/, "", rest)
        if (rest ~ /secrets\./) {
          printf "::error file=%s,line=%d::release.yml reads secrets only in a job with environment: release (docs/dev/signing.md)\n", file, FNR
          printf "%s:%d: %s\n", file, FNR, $0
          bad = 1
        }
      }
    }
    END { exit bad }
  ' "$f" "$f"; then
    fail=1
  fi
done

if (( fail )); then exit 1; fi
echo "workflow-policy: ok (${#files[@]} workflows)"
