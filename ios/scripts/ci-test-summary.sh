#!/bin/sh
# Prints the useful part of an xcodebuild test log for ios.yml: suites,
# every test case with its time (passed, failed or skipped), the totals,
# and the log's tail when it failed. A test that failed and then passed
# on a retry (-retry-tests-on-failure) is a flake: each one gets a
# warning annotation and a line in the job summary ($GITHUB_STEP_SUMMARY,
# when set).
#   scripts/ci-test-summary.sh <log> <xcodebuild exit status>
log=$1
status=${2:-0}
grep -E "^Test Suite|^Test Case .*(passed|failed|error|skipped)|error:|^\*\* |Executed [0-9]+ test|Test [a-zA-Z]+ '.*' (failed|skipped)|with [0-9]+ tests? skipped" "$log" | tail -n 2500 || true

# "Test Case '-[Bundle.Class method]' failed" then "... passed": a flake.
flaky=$(sed -nE "s/^Test Case '-\[([A-Za-z0-9_.]+) ([A-Za-z0-9_]+)\]' (passed|failed) .*/\3 \1.\2/p" "$log" \
  | awk '$1 == "failed" { failed[$2] = 1 } $1 == "passed" && ($2 in failed) { print $2 }' | sort -u)
if [ -n "$flaky" ]; then
  echo "Passed only on a retry:"
  for t in $flaky; do
    echo "  $t"
    echo "::warning title=Flaky test::$t failed, then passed on a retry"
  done
  if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    {
      echo "**Passed only on a retry** ($(basename "$log")):"
      for t in $flaky; do echo "- \`$t\`"; done
    } >> "$GITHUB_STEP_SUMMARY"
  fi
fi

if [ "$status" -ne 0 ]; then tail -n 120 "$log"; fi
exit "$status"
