#!/bin/sh
# Test double for codex/claude, used by internal/cli's tests instead of a
# real CLI. Behavior is selected entirely by env vars so one script can
# stand in for both backends:
#
#   FAKE_EXIT             exit code (default 0)
#   FAKE_SLEEP             seconds to sleep before responding
#   FAKE_GRANDCHILD_FILE    when set, spawn `sleep 100 &` and write its pid
#                            there (used to prove a timeout kills the whole
#                            process group, not just this process)
#   FAKE_BIG                when set, print this many 'A' bytes followed by
#                            a "TAIL_MARKER" line, to exercise output
#                            truncation (the tail must survive)
#   FAKE_CLAUDE_JSON        when set, wrap the answer as
#                            {"result":"...","is_error":<FAKE_IS_ERROR>}
#   FAKE_IS_ERROR            "true" or "false" (default "false")
#   FAKE_STDERR              literal text printed to stderr
#   FAKE_NUM_TURNS           when set (with FAKE_CLAUDE_JSON), adds
#                            "num_turns":<value> to the JSON
#   FAKE_DURATION_MS         when set (with FAKE_CLAUDE_JSON), adds
#                            "duration_ms":<value> to the JSON
#   FAKE_COST_USD             when set (with FAKE_CLAUDE_JSON), adds
#                            "total_cost_usd":<value> to the JSON
#
# The plain-text answer (used when FAKE_BIG is unset) echoes argv, cwd and
# whether HERDR_PANE_ID / CLAUDECODE / CLAUDE_CODE_FOO reached the child,
# so tests can assert template rendering, cwd and env scrubbing.

if [ -n "$FAKE_STDERR" ]; then
  echo "$FAKE_STDERR" >&2
fi

if [ -n "$FAKE_GRANDCHILD_FILE" ]; then
  sleep 100 &
  echo $! > "$FAKE_GRANDCHILD_FILE"
fi

if [ -n "$FAKE_SLEEP" ]; then
  sleep "$FAKE_SLEEP"
fi

if [ -n "$FAKE_BIG" ]; then
  head -c "$FAKE_BIG" /dev/zero | tr '\0' 'A'
  printf 'TAIL_MARKER\n'
  exit "${FAKE_EXIT:-0}"
fi

answer="ARGV:$*;CWD:$(pwd -P);HERDR:${HERDR_PANE_ID:-ABSENT};CLAUDECODE:${CLAUDECODE:-ABSENT};CCFOO:${CLAUDE_CODE_FOO:-ABSENT}"

if [ -n "$FAKE_CLAUDE_JSON" ]; then
  extra=""
  [ -n "$FAKE_NUM_TURNS" ] && extra="$extra,\"num_turns\":$FAKE_NUM_TURNS"
  [ -n "$FAKE_DURATION_MS" ] && extra="$extra,\"duration_ms\":$FAKE_DURATION_MS"
  [ -n "$FAKE_COST_USD" ] && extra="$extra,\"total_cost_usd\":$FAKE_COST_USD"
  printf '{"result":"%s","is_error":%s%s}\n' "$answer" "${FAKE_IS_ERROR:-false}" "$extra"
else
  printf '%s\n' "$answer"
fi

exit "${FAKE_EXIT:-0}"
