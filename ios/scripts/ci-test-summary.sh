#!/bin/sh
# Prints the useful part of an xcodebuild test log for ios.yml: suites,
# every test case with its time (passed, failed or skipped), the totals,
# and the log's tail when it failed.
#   scripts/ci-test-summary.sh <log> <xcodebuild exit status>
log=$1
status=${2:-0}
grep -E "^Test Suite|^Test Case .*(passed|failed|error|skipped)|error:|^\*\* |Executed [0-9]+ test|Test [a-zA-Z]+ '.*' (failed|skipped)|with [0-9]+ tests? skipped" "$log" | tail -n 800 || true
if [ "$status" -ne 0 ]; then tail -n 120 "$log"; fi
exit "$status"
