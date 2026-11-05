#!/usr/bin/env bash
# Put the private environment into a situation: scripts/e2e/scenario.sh <dir> <name> [arg]
#
#   approval              a real answer_prompt pending (question card + approval item)
#   agent-blocked         an agent blocked on a free-form question (no approval card)
#   agent-working         an agent that is working
#   agent-done            that agent finishing (created working first when missing)
#   agent-unread          an agent finishing a turn nobody looked at: attention group done, unread
#   agent-seen            POST seen with that turn: read, and an agent.attention event
#   agent-pin [pane]      POST pin and unpin on an injected agent (or a real pane, with --herdr socket)
#   space-mute            two spaces on the private core, one muted: its approval is not
#                         pushed or spoken, the other's is (needs --herdr socket and --push-stub)
#   label-sync            a real idle Claude on the private core: pin through the API shows
#                         herdr.pinned on `hc get`, labels set with `hc label` show in /api/state
#                         (needs --herdr socket; one agent from the budget)
#   files                 file browser routes on a private folder: list, read, raw image,
#                         hidden names (.env, id_rsa, U+017F, U+200B), symlinks, FIFO, size
#                          and 2,000-entry limits, .. paths, unpaired refusal, files.read log
#   voice-client [mode]   a /ws client holds the voice: connected (default) | speaking | disconnected
#   voice-on              restart the daemon with --voice=fake (voice open, no network)
#   voice-off             restart the daemon with --voice=off
#   daemon-down           stop the private daemon
#   daemon-up             start it again (same port, same voice mode)
#
# Nothing here leaves the private environment; see docs/dev/e2e.md.
set -euo pipefail
# shellcheck source=lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

e2e_load "${1:-}"
NAME="${2:?usage: scenario.sh <dir> <name> [arg]}"
ARG="${3:-}"

post() { e2e_api POST "$1" "$2" >/dev/null; }

daemon_stop() {
  [ -f "$E2E_DIR/daemon.pid" ] && e2e_kill "$(cat "$E2E_DIR/daemon.pid")"
  [ -f "$E2E_DIR/wrap.pid" ] && e2e_kill "$(cat "$E2E_DIR/wrap.pid")"
  rm -f "$E2E_DIR/daemon.pid" "$E2E_DIR/wrap.pid"
  e2e_wait 10 bash -c "! curl -fs --max-time 1 '$E2E_URL/api/state'" || e2e_die "daemon still answers on $E2E_URL"
}

set_voice() {
  E2E_VOICE="$1"
  # Portable in-place edit (BSD sed on macOS and GNU sed on Linux differ on -i).
  sed "s/^export E2E_VOICE=.*/export E2E_VOICE=$1/" "$E2E_DIR/env.sh" >"$E2E_DIR/env.sh.new" && mv "$E2E_DIR/env.sh.new" "$E2E_DIR/env.sh"
}

ws_stop() {
  [ -f "$E2E_DIR/ws.pid" ] && e2e_kill "$(cat "$E2E_DIR/ws.pid")"
  rm -f "$E2E_DIR/ws.pid"
}

# jq_state <filter>: true when the filter is true on /api/state.
jq_state() { e2e_api GET /api/state | jq -e "$1" >/dev/null; }

# agent_attention <pane>: the agent's attention object.
agent_attention() { e2e_api GET /api/state | jq -c --arg p "$1" '.agents[] | select(.pane_id==$p) | .attention'; }

# ensure_finished: e2e:work (build) is injected working and then done, once.
ensure_finished() {
  e2e_api GET /api/state | jq -e '[.agents[] | select(.pane_id=="e2e:work" and .state=="done")] | length > 0' >/dev/null 2>&1 && return 0
  post /api/inject/pane "$(jq -n '{pane_id:"e2e:work",alias:"build",kind:"codex",state:"working",screen:"Running the test suite...\n"}')"
  post /api/inject/state "$(jq -n '{pane_id:"e2e:work",state:"done",screen:"All 42 tests pass. Done.\n"}')"
}

# events <type> [pane]: the event log's lines of that type, newest last.
events() { e2e_api GET "/api/events?type=$1&n=200" | jq -c --arg p "${2:-}" '.[] | select($p == "" or .pane_id == $p)'; }

# jlog <jq-filter>: the filter over every line of the daemon's event files.
jlog() { cat "$E2E_DIR"/data/events/*.jsonl 2>/dev/null | jq -c "$1" 2>/dev/null; }

need_socket() { [ "${E2E_HERDR:-mock}" = socket ] || e2e_die "$NAME needs the private core: up.sh --herdr hc"; }

voice_client_attached() { e2e_api GET /api/state | jq -e '.voice.client_connected == true' >/dev/null; }

scenario_files() {
  local homedir fdir base out code body hdr bin
  homedir=$(cd "$E2E_DIR/home" && pwd)
  fdir="$homedir/files"
  rm -rf "$fdir" "$homedir/outside-secret.txt"
  mkdir -p "$fdir"

  # Populate the private folder with each fixture case.
  printf 'OUTSIDE_SECRET_MARKER\n' >"$homedir/outside-secret.txt"
  printf 'hello files\nline two\n' >"$fdir/hello.txt"
  printf '# Files Scenario\n\nMarkdown body.\n' >"$fdir/README.md"
  printf '\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x02\x00\x00\x00\x90wS\xde' >"$fdir/logo.png"
  printf 'SECRET_ENV_VALUE=never-reveal\n' >"$fdir/.env"
  printf 'PRIVATE_KEY_MATERIAL_NEVER_REVEAL\n' >"$fdir/id_rsa"
  printf 'LONGS_KEY_MATERIAL\n' >"$fdir/$(printf 'id_r\xc5\xbfa')"
  printf 'ZWSP_ENV_MATERIAL\n' >"$fdir/$(printf '.\xe2\x80\x8benv')"
  printf 'ZWSP_KEY_MATERIAL\n' >"$fdir/$(printf 'id_\xe2\x80\x8brsa')"
  ln -s ../outside-secret.txt "$fdir/outside"
  ln -s .env "$fdir/env-link"
  mkfifo "$fdir/pipe.fifo"
  dd if=/dev/zero bs=1048576 count=3 2>/dev/null | tr '\0' 'a' >"$fdir/big.txt"
  {
    printf '\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01'
    dd if=/dev/zero bs=1048576 count=17 2>/dev/null
  } >"$fdir/huge.png"
  (cd "$fdir" && touch z{1..3000}.txt)

  post /api/inject/pane "$(jq -n --arg cwd "$fdir" '{pane_id:"e2e:files",alias:"filer",kind:"codex",state:"idle",cwd:$cwd,screen:"Ready.\n"}')"
  base="/api/agents/e2e:files/files"

  if [ "${E2E_HERDR:-mock}" = socket ]; then
    e2e_hc get space-files --json >/dev/null 2>&1 ||
      e2e_hc run --bg --no-parent --name space-files --cwd "$fdir" --label "herdr.space=sF" -- sleep 3000 >/dev/null
    e2e_wait 15 bash -c "curl -fs '$E2E_URL/api/spaces' | jq -e '[.spaces[].id] | index(\"sF\") != null'" \
      || e2e_die "files: space sF never showed up in GET /api/spaces"
  fi

  # 1. List works, caps at 2,000 entries with truncated: true, marks outside link blocked,
  #    omits FIFO, and hides .env / id_rsa (including U+017F and U+200B spellings).
  out=$(e2e_api GET "$base") || e2e_die "files: GET $base failed"
  echo "$out" | jq -e '.root_name == "files" and .path == "" and .truncated == true and (.entries | length) == 2000 and .hidden >= 4' >/dev/null \
    || e2e_die "files: list did not return 2000 truncated entries with hidden>=4: $(echo "$out" | jq -c '{root_name,path,truncated,len:(.entries|length),hidden}')"
  echo "$out" | jq -e '
    ([.entries[] | select(.name=="hello.txt" and .kind=="file")] | length == 1) and
    ([.entries[] | select(.name=="README.md" and .kind=="file")] | length == 1) and
    ([.entries[] | select(.name=="logo.png" and .kind=="file")] | length == 1) and
    ([.entries[] | select(.name=="big.txt" and .kind=="file" and .size==3145728)] | length == 1) and
    ([.entries[] | select(.name=="huge.png" and .kind=="file")] | length == 1) and
    ([.entries[] | select(.name=="env-link" and .kind=="link")] | length == 1) and
    ([.entries[] | select(.name=="outside" and .kind=="link" and .blocked==true)] | length == 1) and
    ([.entries[] | select(.name=="pipe.fifo")] | length == 0) and
    ([.entries[].name | select(. == ".env" or . == "id_rsa" or . == "id_r\u017fa" or . == ".\u200benv" or . == "id_\u200brsa")] | length == 0)
  ' >/dev/null || e2e_die "files: list entries missing expected files or leaked hidden/FIFO entries"

  # Search finds normal files and never leaks hidden names or U+017F / U+200B spellings.
  e2e_api GET "$base?q=README" | jq -e '[.entries[].path] == ["README.md"]' >/dev/null \
    || e2e_die "files: search ?q=README did not return README.md"
  for q in ".env" "id_r" "id_rsa" "id_r%C5%BFa" ".%E2%80%8Benv" "id_%E2%80%8Brsa"; do
    e2e_api GET "$base?q=$q" | jq -e '(.entries | length) == 0' >/dev/null \
      || e2e_die "files: search ?q=$q leaked a sensitive file"
  done

  # 2. Read works for normal text, Markdown, and image (JSON + ?raw=1 bytes).
  e2e_api GET "$base/hello.txt" | jq -e '.kind == "text" and .path == "hello.txt" and .truncated == false and .text == "hello files\nline two\n"' >/dev/null \
    || e2e_die "files: read hello.txt failed"
  e2e_api GET "$base/README.md" | jq -e '.kind == "text" and .path == "README.md" and .truncated == false and .text == "# Files Scenario\n\nMarkdown body.\n"' >/dev/null \
    || e2e_die "files: read README.md failed"
  e2e_api GET "$base/logo.png" | jq -e '.kind == "image" and .path == "logo.png" and (.text == null)' >/dev/null \
    || e2e_die "files: JSON read logo.png did not report kind image without text"
  hdr="$E2E_DIR/files-logo.hdr"
  bin="$E2E_DIR/files-logo.bin"
  curl -fsS --max-time 5 -D "$hdr" -o "$bin" "$E2E_URL$base/logo.png?raw=1" \
    || e2e_die "files: GET logo.png?raw=1 failed"
  cmp -s "$fdir/logo.png" "$bin" || e2e_die "files: logo.png?raw=1 bytes do not match disk"
  grep -qi '^content-type: image/png' "$hdr" || e2e_die "files: logo.png?raw=1 missing Content-Type: image/png"
  grep -qi '^x-content-type-options: nosniff' "$hdr" || e2e_die "files: logo.png?raw=1 missing nosniff header"
  grep -qi '^cache-control: no-store' "$hdr" || e2e_die "files: logo.png?raw=1 missing Cache-Control: no-store"

  if [ "${E2E_HERDR:-mock}" = socket ]; then
    e2e_api GET /api/spaces/sF/files | jq -e '.root_name == "files" and .truncated == true and (.entries | length) == 2000' >/dev/null \
      || e2e_die "files: space list /api/spaces/sF/files failed"
    e2e_api GET /api/spaces/sF/files/README.md | jq -e '.kind == "text" and .path == "README.md"' >/dev/null \
      || e2e_die "files: space read /api/spaces/sF/files/README.md failed"
  fi

  # 3. Hidden names (.env, id_rsa) and U+017F / U+200B spellings read as 403 sensitive, as does symlink to .env.
  for p in ".env" ".env?raw=1" "id_rsa" "id_r%C5%BFa" ".%E2%80%8Benv" "id_%E2%80%8Brsa" "env-link"; do
    out=$(curl -sS --max-time 5 -w '\n%{http_code}' "$E2E_URL$base/$p") || e2e_die "files: curl $p failed"
    code=$(echo "$out" | tail -n 1)
    body=$(echo "$out" | sed '$d')
    [ "$code" = "403" ] || e2e_die "files: expected 403 on $p, got $code ($body)"
    echo "$body" | jq -e '.reason == "sensitive"' >/dev/null \
      || e2e_die "files: expected reason=sensitive on $p, got $body"
  done

  # 4. Symlink out of folder reads (and lists) as 403 outside.
  for p in "$base/outside" "$base?path=outside"; do
    out=$(curl -sS --max-time 5 -w '\n%{http_code}' "$E2E_URL$p") || e2e_die "files: curl $p failed"
    code=$(echo "$out" | tail -n 1)
    body=$(echo "$out" | sed '$d')
    [ "$code" = "403" ] || e2e_die "files: expected 403 on $p, got $code ($body)"
    echo "$body" | jq -e '.reason == "outside"' >/dev/null \
      || e2e_die "files: expected reason=outside on $p, got $body"
  done

  # 5. FIFO does not hang and returns 403 not_file.
  out=$(curl -sS --max-time 3 -w '\n%{http_code}' "$E2E_URL$base/pipe.fifo") \
    || e2e_die "files: FIFO read timed out or failed"
  code=$(echo "$out" | tail -n 1)
  body=$(echo "$out" | sed '$d')
  [ "$code" = "403" ] || e2e_die "files: expected 403 on pipe.fifo, got $code ($body)"
  echo "$body" | jq -e '.reason == "not_file"' >/dev/null \
    || e2e_die "files: expected reason=not_file on pipe.fifo, got $body"

  # 6. 3 MB text file is truncated to 1 MiB (1,048,576 bytes).
  e2e_api GET "$base/big.txt" | jq -e '.kind == "text" and .size == 3145728 and .truncated == true and (.text | length) == 1048576' >/dev/null \
    || e2e_die "files: 3 MB big.txt was not truncated to 1048576 bytes"

  # 7. 17 MB image is 413 too_large on ?raw=1 (and 200 image metadata on JSON read).
  e2e_api GET "$base/huge.png" | jq -e '.kind == "image" and .size > 16777216 and (.text == null)' >/dev/null \
    || e2e_die "files: huge.png JSON read did not return image metadata"
  out=$(curl -sS --max-time 5 -w '\n%{http_code}' "$E2E_URL$base/huge.png?raw=1") || e2e_die "files: curl huge.png?raw=1 failed"
  code=$(echo "$out" | tail -n 1)
  body=$(echo "$out" | sed '$d')
  [ "$code" = "413" ] || e2e_die "files: expected 413 on huge.png?raw=1, got $code ($body)"
  echo "$body" | jq -e '.reason == "too_large"' >/dev/null \
    || e2e_die "files: expected reason=too_large on huge.png?raw=1, got $body"

  # 8. Paths with .. return 400 bad_path on both list and read.
  for p in "$base/..%2Foutside-secret.txt" "$base?path=..%2Foutside" "$base?path=.."; do
    out=$(curl -sS --max-time 5 -w '\n%{http_code}' "$E2E_URL$p") || e2e_die "files: curl $p failed"
    code=$(echo "$out" | tail -n 1)
    body=$(echo "$out" | sed '$d')
    [ "$code" = "400" ] || e2e_die "files: expected 400 on $p, got $code ($body)"
    echo "$body" | jq -e '.reason == "bad_path"' >/dev/null \
      || e2e_die "files: expected reason=bad_path on $p, got $body"
  done

  # 9. Unpaired requests are refused (401 unauthorized with an unpaired bearer/cookie ticket; 403 host_not_allowed off-loopback Host).
  out=$(curl -sS --max-time 5 -H 'Authorization: Bearer sds_unpaired-ticket' -w '\n%{http_code}' "$E2E_URL$base/hello.txt") \
    || e2e_die "files: curl with unpaired bearer failed"
  code=$(echo "$out" | tail -n 1)
  body=$(echo "$out" | sed '$d')
  [ "$code" = "401" ] || e2e_die "files: expected 401 for unpaired bearer ticket, got $code ($body)"
  echo "$body" | jq -e '.error == "unauthorized"' >/dev/null \
    || e2e_die "files: expected reason=unauthorized for unpaired bearer ticket, got $body"

  out=$(curl -sS --max-time 5 -H 'Cookie: sheepdog_session=unpaired-ticket' -w '\n%{http_code}' "$E2E_URL$base") \
    || e2e_die "files: curl with unpaired cookie failed"
  code=$(echo "$out" | tail -n 1)
  body=$(echo "$out" | sed '$d')
  [ "$code" = "401" ] || e2e_die "files: expected 401 for unpaired session cookie, got $code ($body)"
  echo "$body" | jq -e '.error == "unauthorized"' >/dev/null \
    || e2e_die "files: expected reason=unauthorized for unpaired session cookie, got $body"

  out=$(curl -sS --max-time 5 -H 'Host: example.com' -w '\n%{http_code}' "$E2E_URL$base/hello.txt") \
    || e2e_die "files: curl with non-loopback Host failed"
  code=$(echo "$out" | tail -n 1)
  body=$(echo "$out" | sed '$d')
  [ "$code" = "403" ] || e2e_die "files: expected 403 for non-loopback Host, got $code ($body)"
  echo "$body" | jq -e '.error == "host_not_allowed"' >/dev/null \
    || e2e_die "files: expected reason=host_not_allowed for non-loopback Host, got $body"

  # 10. files.read shows in the event log with metadata and never file content.
  out=$(e2e_api GET "/api/events?type=files.read&n=200") || e2e_die "files: GET /api/events?type=files.read failed"
  echo "$out" | jq -e '
    ([.[] | select(.pane_id=="e2e:files" and .payload.path=="hello.txt" and .payload.raw==false and (.payload.size > 0) and (.payload.reason == null))] | length >= 1) and
    ([.[] | select(.pane_id=="e2e:files" and .payload.path=="logo.png" and .payload.raw==true and (.payload.size > 0))] | length >= 1) and
    ([.[] | select(.pane_id=="e2e:files" and .payload.path==".env" and .payload.reason=="sensitive" and (.payload.size == null))] | length >= 1) and
    ([.[] | select(.pane_id=="e2e:files" and .payload.path=="outside" and .payload.reason=="outside")] | length >= 1) and
    ([.[] | select(.pane_id=="e2e:files" and .payload.path=="pipe.fifo" and .payload.reason=="not_file")] | length >= 1) and
    ([.[] | select(.pane_id=="e2e:files" and .payload.path=="huge.png" and .payload.raw==true and .payload.reason=="too_large")] | length >= 1)
  ' >/dev/null || e2e_die "files: /api/events?type=files.read missing expected metadata records: $out"

  # The event log flushes buffered writes to disk every 500 ms.
  local want_on_disk
  want_on_disk=$(echo "$out" | jq '[.[] | select(.pane_id=="e2e:files")] | length')
  e2e_wait 5 bash -c "cat '$E2E_DIR'/data/events/*.jsonl 2>/dev/null | jq -c 'select(.type==\"files.read\" and .pane_id==\"e2e:files\")' 2>/dev/null | jq -s -e 'length >= 6 and length >= $want_on_disk'" \
    || e2e_die "files: on-disk event log has no files.read lines for e2e:files"
  if jlog 'select(.type=="files.read")' | grep -Eq 'hello files|Files Scenario|SECRET_ENV_VALUE|PRIVATE_KEY_MATERIAL|OUTSIDE_SECRET_MARKER|LONGS_KEY_MATERIAL|ZWSP_ENV_MATERIAL'; then
    e2e_die "files: files.read event log leaked file contents"
  fi

  echo "files: list (2000/3000+ truncated, hidden>=4), read, raw image, sensitive (.env, id_rsa, U+017F, U+200B, symlink), outside symlink, FIFO, 3 MB text truncation, 17 MB image 413, .. 400, unpaired 401/403, and files.read audit events verified"
}

case "$NAME" in
  approval)
    SCREEN="$E2E_ROOT/testdata/herdr/codex-approval-prompt.screen.txt"
    [ -f "$SCREEN" ] || e2e_die "missing $SCREEN"
    post /api/inject/blocked "$(jq -n --rawfile s "$SCREEN" '{pane_id:"e2e:approval",alias:"auth",kind:"codex",screen:$s}')"
    e2e_api GET /api/state | jq -e '[.questions[] | select(.kind=="answer_prompt")] | length >= 1' >/dev/null \
      || e2e_die "no answer_prompt pending after the inject"
    echo "approval: answer_prompt pending for pane e2e:approval (agent auth)"
    ;;
  agent-blocked)
    post /api/inject/pane "$(jq -n '{pane_id:"e2e:blocked",alias:"docs",kind:"claude",state:"blocked",screen:"I can document this two ways.\nWhich one should I use, the short README or the full guide?\n> "}')"
    echo "agent-blocked: agent docs is blocked on a question"
    ;;
  agent-working)
    post /api/inject/pane "$(jq -n '{pane_id:"e2e:work",alias:"build",kind:"codex",state:"working",screen:"Running the test suite...\n"}')"
    echo "agent-working: agent build is working"
    ;;
  agent-done)
    e2e_api GET /api/agents | jq -e '[.. | objects | select(.pane_id? == "e2e:work")] | length > 0' >/dev/null 2>&1 \
      || post /api/inject/pane "$(jq -n '{pane_id:"e2e:work",alias:"build",kind:"codex",state:"working",screen:"Running the test suite...\n"}')"
    post /api/inject/state "$(jq -n '{pane_id:"e2e:work",state:"done",screen:"All 42 tests pass. Done.\n"}')"
    echo "agent-done: agent build finished"
    ;;
  agent-unread)
    ensure_finished
    A=$(agent_attention e2e:work)
    echo "$A" | jq -e '.group == "done" and .unread == true' >/dev/null || e2e_die "e2e:work is not done and unread: $A"
    e2e_api GET /api/state | jq -e '.attention_counts.done_unread >= 1' >/dev/null || e2e_die "attention_counts has no unread done agent"
    echo "agent-unread: build finished a turn nobody looked at: $A"
    ;;
  agent-seen)
    ensure_finished
    TURN=$(agent_attention e2e:work | jq -r '.turn')
    e2e_api POST /api/agents/e2e:work/seen "$(jq -n --argjson t "$TURN" '{turn:$t}')" | jq -e '.ok == true and .attention.unread == false' >/dev/null \
      || e2e_die "POST seen did not mark build seen"
    agent_attention e2e:work | jq -e '.unread == false and .seen == '"$TURN" >/dev/null || e2e_die "/api/state still shows build unread"
    e2e_wait 3 bash -c "curl -fs '$E2E_URL/api/events?type=agent.attention&n=200' | jq -e '[.[] | select(.pane_id==\"e2e:work\" and .payload.unread==false)] | length >= 1'" \
      || e2e_die "no agent.attention event for the seen mark on /events"
    echo "agent-seen: seen turn $TURN: $(agent_attention e2e:work); event: $(events agent.attention e2e:work | tail -1)"
    # Mark as unread brings it back.
    e2e_api POST /api/agents/e2e:work/seen '{"unread":true}' | jq -e '.attention.unread == true' >/dev/null || e2e_die "mark as unread did not take"
    echo "agent-seen: marked unread again: $(agent_attention e2e:work)"
    ;;
  agent-pin)
    PANE="${ARG:-e2e:work}"
    # The injected pane exists only on the mock backend; on hc give a real pane id.
    [ -z "$ARG" ] && [ "${E2E_HERDR:-mock}" = socket ] && e2e_die "agent-pin on hc needs a pane id: scenario.sh $E2E_DIR agent-pin <pane>"
    [ -z "$ARG" ] && ensure_finished
    e2e_api POST "/api/agents/$PANE/pin" '{"pinned":true}' | jq -e '.attention.pinned == true' >/dev/null || e2e_die "pin did not take"
    agent_attention "$PANE" | jq -e '.pinned == true' >/dev/null || e2e_die "/api/state does not show $PANE pinned"
    e2e_wait 3 bash -c "curl -fs '$E2E_URL/api/events?type=agent.attention&n=200' | jq -e '[.[] | select(.pane_id==\"$PANE\" and .payload.pinned==true)] | length >= 1'" \
      || e2e_die "no agent.attention event for the pin on /events"
    echo "agent-pin: $PANE pinned: $(agent_attention "$PANE")"
    if [ -n "$ARG" ] && [ "${E2E_HERDR:-mock}" = socket ]; then
      # A real terminal keeps its pin in a label: it survives the daemon.
      "$0" "$E2E_DIR" daemon-down >/dev/null
      "$0" "$E2E_DIR" daemon-up >/dev/null
      e2e_wait 20 bash -c "curl -fs '$E2E_URL/api/state' | jq -e '[.agents[] | select(.pane_id==\"$PANE\" and .attention.pinned==true)] | length == 1'" \
        || e2e_die "$PANE is not pinned after the daemon restarted"
      echo "agent-pin: still pinned after daemon-down / daemon-up (label on the terminal)"
    else
      echo "agent-pin: an injected pane is in memory and does not outlive the daemon; label-sync checks the restart on a real terminal"
    fi
    ;;
  space-mute)
    need_socket
    [ -n "${E2E_PUSH_RELAY:-}" ] || e2e_die "space-mute needs the push stub: up.sh --push-stub"
    SCREEN="$E2E_ROOT/testdata/herdr/codex-approval-prompt.screen.txt"
    HOMEDIR=$(cd "$E2E_DIR/home" && pwd)
    mkdir -p "$HOMEDIR/muted" "$HOMEDIR/loud"
    # Two spaces on the private core: a terminal with the herdr.space label is a space.
    for sp in muted:sM loud:sL; do
      e2e_hc get "space-${sp%%:*}" --json >/dev/null 2>&1 ||
        e2e_hc run --bg --no-parent --name "space-${sp%%:*}" --cwd "$HOMEDIR/${sp%%:*}" --label "herdr.space=${sp##*:}" -- sleep 3000 >/dev/null
    done
    e2e_wait 15 bash -c "curl -fs '$E2E_URL/api/spaces' | jq -e '[.spaces[].id] | (index(\"sM\") != null and index(\"sL\") != null)'" || e2e_die "the two spaces never showed up in GET /api/spaces"
    MPATH=$(e2e_api GET /api/spaces | jq -r '.spaces[] | select(.id=="sM") | .path')
    LPATH=$(e2e_api GET /api/spaces | jq -r '.spaces[] | select(.id=="sL") | .path')
    # The stub phone: registered with a relay token the stub gave out.
    e2e_api POST /api/push/register '{"relay_token":"e2e-stub-relay-token-0001","environment":"sandbox","device":"stub phone"}' | jq -e '.ok == true' >/dev/null || e2e_die "could not register the stub phone"
    e2e_api POST /api/spaces/sM/mute '{"muted":true}' | jq -e '.ok == true and .space.muted == true' >/dev/null || e2e_die "mute did not take"
    e2e_api GET /api/spaces | jq -e '[.spaces[] | select(.id=="sM" and .muted==true)] | length == 1' >/dev/null || e2e_die "GET /api/spaces does not show sM muted"
    e2e_api GET /api/state | jq -e --arg p "$MPATH" '[.space_mutes[] | select(.path==$p)] | length == 1' >/dev/null || e2e_die "/api/state has no space_mutes entry"
    SENT0=$(wc -l <"$E2E_PUSH_LOG")
    # An approval in the muted space.
    post /api/inject/blocked "$(jq -n --rawfile s "$SCREEN" --arg cwd "$MPATH" '{pane_id:"e2e:muted-approval",alias:"mutedauth",kind:"codex",cwd:$cwd,screen:$s}')"
    sleep 3
    [ "$(wc -l <"$E2E_PUSH_LOG")" = "$SENT0" ] || e2e_die "the relay stub was sent a push for the muted space: $(tail -1 "$E2E_PUSH_LOG")"
    [ -z "$(jlog 'select(.type=="push.sent" and .pane_id=="e2e:muted-approval")')" ] || e2e_die "push.sent for the muted space"
    # Silent context (the agent's name for the voice) is not speech; an idle or interrupt line is.
    [ -z "$(jlog 'select(.type=="voice.push" and .payload.urgency != "silent" and (.payload.text // "" | test("mutedauth")))')" ] || e2e_die "the voice was pushed a spoken line about the muted space"
    jlog 'select(.type=="supervisor.push_quiet" and .pane_id=="e2e:muted-approval")' | jq -e '.payload.reason == "space_muted"' >/dev/null || e2e_die "no supervisor.push_quiet reason space_muted"
    jlog 'select(.type=="pending.created" and .pane_id=="e2e:muted-approval")' | jq -e '.payload.muted == true' >/dev/null || e2e_die "pending.created lacks muted: true"
    e2e_api GET /api/state | jq -e '[.questions[] | select(.kind=="answer_prompt" and .agent=="mutedauth")] | length == 1' >/dev/null || e2e_die "the question card of the muted space is gone"
    echo "space-mute: muted space: no push to the stub ($SENT0 lines before and after), no push.sent, no voice line; push_quiet space_muted; pending.created muted:true; the question card is still in /api/state"
    # An approval in the other space still goes out.
    post /api/inject/blocked "$(jq -n --rawfile s "$SCREEN" --arg cwd "$LPATH" '{pane_id:"e2e:loud-approval",alias:"loudauth",kind:"codex",cwd:$cwd,screen:$s}')"
    e2e_wait 10 bash -c "[ \$(wc -l <'$E2E_PUSH_LOG') -gt $SENT0 ]" || e2e_die "the unmuted space's approval never reached the relay stub"
    tail -1 "$E2E_PUSH_LOG" | jq -e '.kind == "approval" and .priority == 10' >/dev/null || e2e_die "the stub got something other than an approval: $(tail -1 "$E2E_PUSH_LOG")"
    # push.sent is logged just after the relay answers.
    e2e_wait 5 bash -c "cat '$E2E_DIR'/data/events/*.jsonl | jq -e 'select(.type==\"push.sent\" and .pane_id==\"e2e:loud-approval\" and .payload.kind==\"approval\")'" || e2e_die "no push.sent for the unmuted space"
    jlog 'select(.type=="pending.created" and .pane_id=="e2e:loud-approval")' | jq -e '.payload.muted == null' >/dev/null || e2e_die "pending.created of the unmuted space says muted"
    echo "space-mute: unmuted space: the stub got $(tail -1 "$E2E_PUSH_LOG"); push.sent logged"
    ;;
  label-sync)
    need_socket
    command -v claude >/dev/null 2>&1 || e2e_die "label-sync needs a claude CLI"
    claude auth status >/dev/null 2>&1 || e2e_die "claude is not logged in"
    "$E2E_ROOT/scripts/e2e/budget.sh" reserve agent 1 || e2e_die "agent budget used up for today"
    REPO="/tmp/sheepdog-nightly-agent/claude"
    if [ ! -d "$REPO/.git" ]; then
      mkdir -p "$REPO"
      (cd "$REPO" && git init -q && printf '# throwaway\n' >README.md && git add README.md &&
        git -c user.name=nightly -c user.email=nightly@localhost commit -qm init) || e2e_die "could not make $REPO"
    fi
    # Claude Code at its prompt, with no task: it spends no tokens.
    RUN=$(cd "$REPO" && e2e_hc run --json --bg --no-parent --name "labelsync" --cwd "$REPO" -- claude --model haiku 2>&1) || e2e_die "hc run: $RUN"
    TERM_ID=$(echo "$RUN" | jq -r '.address // empty')
    [ -n "$TERM_ID" ] || e2e_die "no terminal address in: $RUN"
    echo "$TERM_ID" >"$E2E_DIR/labelsync.term"
    e2e_wait 90 bash -c "curl -fs '$E2E_URL/api/state' | jq -e '[.agents[] | select(.kind==\"claude\")] | length == 1'" || e2e_die "the claude agent never showed up in /api/state"
    PANE=$(e2e_api GET /api/state | jq -r '[.agents[] | select(.kind=="claude")][0].pane_id')
    echo "label-sync: agent $PANE in terminal $TERM_ID: $(agent_attention "$PANE")"
    agent_attention "$PANE" | jq -e '.shared == true' >/dev/null || e2e_die "the marks of an hc terminal should be shared"
    # API to labels.
    e2e_api POST "/api/agents/$PANE/pin" '{"pinned":true}' | jq -e '.attention.pinned == true' >/dev/null || e2e_die "pin did not take"
    e2e_hc get "$TERM_ID" --json | jq -e '.labels["herdr.pinned"] == "1"' >/dev/null || e2e_die "hc get shows no herdr.pinned=1: $(e2e_hc get "$TERM_ID" --json | jq -c .labels)"
    echo "label-sync: pinned through the API; hc get labels: $(e2e_hc get "$TERM_ID" --json | jq -c .labels)"
    e2e_api POST "/api/agents/$PANE/seen" '{"turn":0}' | jq -e '.ok == true' >/dev/null || e2e_die "seen did not take"
    e2e_hc get "$TERM_ID" --json | jq -e '.labels["herdr.seen"] != null' >/dev/null || e2e_die "hc get shows no herdr.seen after the seen mark"
    # Labels to API: another Herdr client unpins and sets seen; the state follows within 2 s.
    e2e_hc label "$TERM_ID" herdr.pinned= herdr.seen=0 >/dev/null
    e2e_wait 2 bash -c "curl -fs '$E2E_URL/api/state' | jq -e '[.agents[] | select(.pane_id==\"$PANE\" and .attention.pinned==false and .attention.seen==0)] | length == 1'" \
      || e2e_die "/api/state did not follow hc label within 2 s: $(agent_attention "$PANE")"
    echo "label-sync: hc label herdr.pinned= herdr.seen=0 -> state followed: $(agent_attention "$PANE")"
    e2e_wait 3 bash -c "curl -fs '$E2E_URL/api/events?type=agent.attention&n=200' | jq -e '[.[] | select(.pane_id==\"$PANE\" and .payload.via==\"herdr\")] | length >= 1'" \
      || e2e_die "no agent.attention event with via herdr for the label change"
    # The pin outlives the daemon: it is a label on the terminal.
    e2e_api POST "/api/agents/$PANE/pin" '{"pinned":true}' >/dev/null
    "$0" "$E2E_DIR" daemon-down >/dev/null
    "$0" "$E2E_DIR" daemon-up >/dev/null
    e2e_wait 30 bash -c "curl -fs '$E2E_URL/api/state' | jq -e '[.agents[] | select(.pane_id==\"$PANE\" and .attention.pinned==true)] | length == 1'" || e2e_die "the pin did not survive the daemon restarting"
    echo "label-sync: pin survived daemon-down / daemon-up: $(agent_attention "$PANE")"
    e2e_hc kill "$TERM_ID" >/dev/null 2>&1 || true
    ;;
  files)
    scenario_files
    ;;
  voice-on)
    daemon_stop
    set_voice fake
    e2e_daemon_start
    e2e_wait 15 bash -c "curl -fs '$E2E_URL/api/state' | jq -e '.voice.status == \"connected\"'" || e2e_die "fake voice never reported connected"
    echo "voice-on: daemon restarted with --voice=fake; voice connected"
    ;;
  voice-off)
    ws_stop
    daemon_stop
    set_voice off
    e2e_daemon_start
    echo "voice-off: daemon restarted with --voice=off"
    ;;
  voice-client)
    MODE="${ARG:-connected}"
    case "$MODE" in
      disconnected)
        ws_stop
        e2e_wait 10 bash -c "curl -fs '$E2E_URL/api/state' | jq -e '.voice.client_connected == false'" || e2e_die "client still attached"
        echo "voice-client: disconnected"
        ;;
      connected|speaking)
        if [ "$MODE" = speaking ] && [ "${E2E_VOICE:-off}" != fake ]; then
          e2e_die "speaking needs a voice: run 'scenario.sh $E2E_DIR voice-on' first"
        fi
        ws_stop
        BIN="$E2E_DIR/wsclient"
        [ -x "$BIN" ] || (cd "$E2E_ROOT" && go build -o "$BIN" ./scripts/e2e/wsclient)
        SAY=()
        [ "$MODE" = speaking ] && SAY=(-say "e2e speaking check")
        nohup "$BIN" -url "ws://127.0.0.1:$E2E_PORT/ws" -max 20m ${SAY[@]+"${SAY[@]}"} >"$E2E_DIR/ws.log" 2>&1 &
        echo $! >"$E2E_DIR/ws.pid"
        e2e_wait 10 voice_client_attached || e2e_die "client never attached; see $E2E_DIR/ws.log"
        if [ "$MODE" = speaking ]; then
          # The fake voice speaks for 6 s after the text message.
          e2e_wait 6 bash -c "curl -fs '$E2E_URL/api/state' | jq -e '.voice.speaking == true'" || e2e_die "voice never reported speaking"
        fi
        echo "voice-client: $MODE (macos client, pid $(cat "$E2E_DIR/ws.pid"))"
        ;;
      *) e2e_die "voice-client mode: connected | speaking | disconnected";;
    esac
    ;;
  daemon-down)
    ws_stop
    daemon_stop
    echo "daemon-down: $E2E_URL no longer answers"
    ;;
  daemon-up)
    if curl -fs --max-time 1 "$E2E_URL/api/state" >/dev/null 2>&1; then echo "daemon-up: already up"; exit 0; fi
    e2e_daemon_start
    echo "daemon-up: $E2E_URL answers again"
    ;;
  *) e2e_die "unknown scenario: $NAME";;
esac
