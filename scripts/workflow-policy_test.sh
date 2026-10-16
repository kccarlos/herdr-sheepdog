#!/usr/bin/env bash
# Tests scripts/workflow-policy.sh against fixture workflows, then against
# the repository's own .github/workflows.
# Run: bash scripts/workflow-policy_test.sh
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
policy="$here/workflow-policy.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
fails=0
n=0

# expect <pass|fail> <name> <file name> <<'EOF' ...workflow... EOF
# Each case gets its own directory with only that workflow in it.
expect() {
  local want="$1" name="$2" file="$3" d got
  n=$((n + 1)); d="$work/case$n"; mkdir -p "$d"
  cat > "$d/$file"
  if "$policy" "$d" > "$d/out" 2>&1; then got=pass; else got=fail; fi
  if [[ "$got" == "$want" ]]; then echo "ok   $name"; else
    echo "FAIL $name (want $want, got $got)"; sed 's/^/     /' "$d/out"; fails=$((fails + 1))
  fi
}

expect pass "linux-only ci.yml" ci.yml <<'EOF'
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - run: make test
EOF

expect fail "macOS runner in ci.yml" ci.yml <<'EOF'
jobs:
  test:
    runs-on: macos-15
EOF

expect fail "self-hosted macOS label in ci.yml" ci.yml <<'EOF'
jobs:
  test:
    runs-on: [self-hosted, macOS]
EOF

expect fail "xcodebuild in ci.yml" ci.yml <<'EOF'
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - run: xcodebuild test
EOF

expect fail "macOS runner in ci.yml even with environment: release" ci.yml <<'EOF'
jobs:
  sign:
    runs-on: macos-15
    environment: release
EOF

expect pass "macOS and xcodebuild in ios.yml" ios.yml <<'EOF'
jobs:
  unit:
    runs-on: macos-26
    steps:
      - run: xcodebuild -scheme X test
EOF

expect fail "secret in ios.yml" ios.yml <<'EOF'
jobs:
  unit:
    runs-on: macos-26
    env:
      K: ${{ secrets.ASC_KEY }}
EOF

expect fail "GITHUB_TOKEN in ios.yml" ios.yml <<'EOF'
jobs:
  unit:
    runs-on: macos-26
    env:
      GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}
EOF

expect pass "release.yml: macOS and secrets in the environment job" release.yml <<'EOF'
on:
  push:
    tags: ["v*"]
jobs:
  release:
    runs-on: ubuntu-latest
    env:
      GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}
  sign-macos:
    needs: release
    runs-on: macos-15
    environment: release
    steps:
      - env:
          P12: ${{ secrets.MACOS_CERT_P12_BASE64 }}
        run: ./sign.sh
  homebrew:
    needs: sign-macos
    runs-on: ubuntu-latest
    environment:
      name: release
    env:
      T: ${{ secrets.HOMEBREW_TAP_TOKEN }}
EOF

expect pass "release.yml: quoted environment name" release.yml <<'EOF'
jobs:
  sign-macos:
    runs-on: macos-15
    environment: "release" # approval gate
EOF

expect fail "release.yml: macOS job without an environment" release.yml <<'EOF'
jobs:
  sign-macos:
    runs-on: macos-15
EOF

expect fail "release.yml: environment on another job than the macOS one" release.yml <<'EOF'
jobs:
  sign-macos:
    runs-on: macos-15
  homebrew:
    runs-on: ubuntu-latest
    environment: release
EOF

expect fail "release.yml: another environment" release.yml <<'EOF'
jobs:
  sign-macos:
    runs-on: macos-15
    environment: staging
EOF

expect fail "release.yml: environment name that only starts with release" release.yml <<'EOF'
jobs:
  sign-macos:
    runs-on: macos-15
    environment:
      name: release-test
EOF

expect fail "release.yml: environment set in a step, not on the job" release.yml <<'EOF'
jobs:
  sign-macos:
    runs-on: macos-15
    steps:
      - name: x
        env:
          environment: release
EOF

expect fail "release.yml: macOS at the top level" release.yml <<'EOF'
env:
  RUNNER: macos-15
jobs:
  sign-macos:
    runs-on: ${{ env.RUNNER }}
    environment: release
EOF

expect fail "release.yml: signing secret outside the environment job" release.yml <<'EOF'
jobs:
  release:
    runs-on: ubuntu-latest
    env:
      P12: ${{ secrets.MACOS_CERT_P12_BASE64 }}
  sign-macos:
    runs-on: macos-15
    environment: release
EOF

expect pass "release.yml: GITHUB_TOKEN outside the environment job" release.yml <<'EOF'
jobs:
  release:
    runs-on: ubuntu-latest
    env:
      GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}
EOF

expect fail "macOS in another workflow's environment: release job" relay.yml <<'EOF'
jobs:
  deploy:
    runs-on: macos-15
    environment: release
EOF

# The repository's own workflows must pass.
if out="$("$policy" "$here/../.github/workflows" 2>&1)"; then echo "ok   the repository's workflows"; else
  echo "FAIL the repository's workflows"; printf '%s\n' "$out"; fails=$((fails + 1))
fi

if (( fails )); then echo "$fails failed"; exit 1; fi
echo "all passed"
