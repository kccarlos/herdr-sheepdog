#!/bin/sh
# Runs tests from the shared build for ios.yml, then prints the summary
# (scripts/ci-test-summary.sh):
#   scripts/ci-xctest.sh <name> <udid> <xcodebuild test arguments...>
# writes $RESULTS/<name>.log and $RESULTS/<name>.xcresult. A failing test is
# run once more (-test-iterations 2). When the test runner itself never
# starts ("never finished bootstrapping": a simulator that has just booted
# can stall it), no test has run, so the whole run is started once more.
name=$1
udid=$2
shift 2
log="$RESULTS/$name.log"
run() {
  rm -rf "$RESULTS/$name.xcresult"
  xcodebuild test-without-building \
    -xctestrun "$XCTESTRUN" \
    -destination "id=$udid" \
    -collect-test-diagnostics never \
    -retry-tests-on-failure -test-iterations 2 \
    -resultBundlePath "$RESULTS/$name.xcresult" \
    "$@" > "$log" 2>&1
}
status=0
run "$@" || status=$?
if [ "$status" -ne 0 ] && grep -q "never finished bootstrapping" "$log" && ! grep -q "^Test Case " "$log"; then
  echo "::warning title=Test runner::the test runner never started ($name); running the tests once more"
  mv "$log" "$RESULTS/$name.first.log"
  status=0
  run "$@" || status=$?
fi
sh "$(dirname "$0")/ci-test-summary.sh" "$log" "$status"
