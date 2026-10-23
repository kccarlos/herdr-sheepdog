#!/usr/bin/env bash
# Tests scripts/ci-plan.sh: the tiers, the shard check (against fixtures and
# against the real ci-shards.txt) and the matrix.
# Run: bash scripts/ci-plan_test.sh
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
plan="$here/ci-plan.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
fails=0

ok() { echo "ok   $1"; }
bad() { echo "FAIL $1"; fails=$((fails + 1)); }

# tier <want> <name> <paths...>
tier() {
  local want="$1" name="$2" got
  shift 2
  got="$(printf '%s\n' "$@" | bash "$plan" tier)"
  if [[ "$got" == "$want" ]]; then ok "tier $name"; else bad "tier $name (want $want, got $got)"; fi
}

tier none "docs only" docs/iphone-app.md README.md
tier none "app docs only" ios/README.md ios/docs/dev/status.md
tier none "Go outside the API" internal/brain/brain.go
tier none "no paths"
tier smoke "the daemon's web API" internal/web/server.go
tier smoke "a wire golden" testdata/protocol/ws/hello.json
tier smoke "go.mod" go.mod docs/dev/development.md
tier full "app source" ios/Sources/Sheepdog/App.swift
tier full "the shard list" ios/scripts/ci-shards.txt
tier full "ios.yml" .github/workflows/ios.yml
tier full "app and API" internal/web/server.go ios/project.yml

# A fixture UI test tree: three classes, one with two methods.
tests="$work/uitests"
mkdir -p "$tests/sub"
cat > "$tests/A.swift" <<'EOF'
final class AUITests: XCTestCase {
    func testOne() {}
    func testTwo() {}
}
EOF
printf 'class BUITests : XCTestCase {}\n' > "$tests/sub/B.swift"
printf 'final class CUITests: XCTestCase {}\nfinal class Helper {}\n' > "$tests/C.swift"
# The same tree plus an iPad class, for the one-simulator-per-job rule.
ipad_tests="$work/uitests-ipad"
cp -R "$tests" "$ipad_tests"
printf 'final class IPadDUITests: XCTestCase {\n    func testWide() {}\n}\n' > "$ipad_tests/D.swift"

# [tree=<dir>] check <pass|fail> <name> <<'EOF' ...shard list... EOF
check() {
  local want="$1" name="$2" got
  cat > "$work/shards"
  if bash "$plan" check "$work/shards" "${tree:-$tests}" > "$work/out" 2>&1; then got=pass; else got=fail; fi
  if [[ "$got" == "$want" ]]; then ok "check $name"; else
    bad "check $name (want $want, got $got)"; sed 's/^/     /' "$work/out"
  fi
}

check pass "every class once" <<'EOF'
smoke    SheepdogKitTests
smoke    SheepdogUITests/AUITests/testOne   # a comment
shard-1  SheepdogUITests/AUITests
shard-2  SheepdogUITests/BUITests
shard-2  SheepdogUITests/CUITests
EOF

check fail "a class in no job" <<'EOF'
smoke    SheepdogKitTests
shard-1  SheepdogUITests/AUITests
shard-1  SheepdogUITests/BUITests
EOF

check fail "a class in two jobs" <<'EOF'
smoke    SheepdogKitTests
smoke    SheepdogUITests/AUITests
shard-1  SheepdogUITests/AUITests
shard-1  SheepdogUITests/BUITests
shard-1  SheepdogUITests/CUITests
EOF

check fail "a class that does not exist" <<'EOF'
smoke    SheepdogKitTests
shard-1  SheepdogUITests/AUITests
shard-1  SheepdogUITests/BUITests
shard-1  SheepdogUITests/CUITests
shard-1  SheepdogUITests/GoneUITests
EOF

check fail "a method that does not exist" <<'EOF'
smoke    SheepdogKitTests
smoke    SheepdogUITests/AUITests/testGone
shard-1  SheepdogUITests/AUITests
shard-1  SheepdogUITests/BUITests
shard-1  SheepdogUITests/CUITests
EOF

check fail "only a method of a class" <<'EOF'
smoke    SheepdogKitTests
smoke    SheepdogUITests/AUITests/testOne
shard-1  SheepdogUITests/BUITests
shard-1  SheepdogUITests/CUITests
EOF

check fail "no unit tests in smoke" <<'EOF'
shard-1  SheepdogUITests/AUITests
shard-1  SheepdogUITests/BUITests
shard-1  SheepdogUITests/CUITests
EOF

check fail "a job name the workflow does not know" <<'EOF'
smoke    SheepdogKitTests
extra    SheepdogUITests/AUITests
shard-1  SheepdogUITests/BUITests
shard-1  SheepdogUITests/CUITests
EOF

tree="$ipad_tests" check pass "an iPad class in a job of its own" <<'EOF'
smoke    SheepdogKitTests
shard-1  SheepdogUITests/AUITests
shard-1  SheepdogUITests/BUITests
shard-1  SheepdogUITests/CUITests
shard-2  SheepdogUITests/IPadDUITests
EOF

tree="$ipad_tests" check fail "an iPad class with iPhone tests" <<'EOF'
smoke    SheepdogKitTests
shard-1  SheepdogUITests/AUITests
shard-1  SheepdogUITests/BUITests
shard-1  SheepdogUITests/CUITests
shard-1  SheepdogUITests/IPadDUITests
EOF

tree="$ipad_tests" check fail "an iPad method in smoke" <<'EOF'
smoke    SheepdogKitTests
smoke    SheepdogUITests/IPadDUITests/testWide
shard-1  SheepdogUITests/AUITests
shard-1  SheepdogUITests/BUITests
shard-1  SheepdogUITests/CUITests
shard-2  SheepdogUITests/IPadDUITests
EOF

if bash "$plan" check > "$work/out" 2>&1; then ok "check the real ci-shards.txt"; else
  bad "check the real ci-shards.txt"; sed 's/^/     /' "$work/out"
fi

# The matrix: in the full tier the smoke method is left to the job that
# runs its whole class, the iPad classes go to the iPad, the two special
# classes become flags, and the job the build line names (else the first)
# builds.
cat > "$work/shards" <<'EOF'
build    shard-1
smoke    SheepdogKitTests
smoke    SheepdogUITests/HistoryApprovalUITests
smoke    SheepdogUITests/CardsUITests/testCards
shard-1  SheepdogUITests/IPadUITests
shard-2  SheepdogUITests/ReconnectUITests
shard-2  SheepdogUITests/CardsUITests
EOF
want_full='{"include":[{"name":"smoke","iphone":"-only-testing:SheepdogKitTests","ipad":"","history":true,"reconnect":false,"build":false},{"name":"shard-1","iphone":"","ipad":"-only-testing:SheepdogUITests/IPadUITests","history":false,"reconnect":false,"build":true},{"name":"shard-2","iphone":"-only-testing:SheepdogUITests/CardsUITests","ipad":"","history":false,"reconnect":true,"build":false}]}'
want_smoke='{"include":[{"name":"smoke","iphone":"-only-testing:SheepdogKitTests -only-testing:SheepdogUITests/CardsUITests/testCards","ipad":"","history":true,"reconnect":false,"build":true}]}'
got="$(bash "$plan" matrix full "$work/shards")"
if [[ "$got" == "$want_full" ]]; then ok "matrix full"; else bad "matrix full"; echo "     got  $got"; echo "     want $want_full"; fi
got="$(bash "$plan" matrix smoke "$work/shards")"
if [[ "$got" == "$want_smoke" ]]; then ok "matrix smoke"; else bad "matrix smoke"; echo "     got  $got"; echo "     want $want_smoke"; fi

# The real list: one builder per tier.
for t in full smoke; do
  n="$(bash "$plan" matrix "$t" | grep -o '"build":true' | wc -l | tr -d ' ')"
  if [[ "$n" == 1 ]]; then ok "one builder in the real $t matrix"; else bad "the real $t matrix has $n builders"; fi
done

if (( fails )); then echo "ci-plan_test: $fails failed"; exit 1; fi
echo "ci-plan_test: ok"
