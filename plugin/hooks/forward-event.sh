#!/bin/sh
# herdr plugin hook (docs/herdr-protocol.md §8): one process per event,
# spawned from the herdr app thread with the event in
# $HERDR_PLUGIN_EVENT_JSON. Forwards it as-is to the sheepdog daemon's
# hook channel (POST /api/herdr/event, see internal/web/api.go), and
# never fails the hook or blocks herdr: curl has a short timeout, and
# every exit path here is 0.
curl -s -m 2 -X POST -H 'Content-Type: application/json' \
  --data-binary "$HERDR_PLUGIN_EVENT_JSON" \
  "http://127.0.0.1:${SHEEPDOG_PORT:-7777}/api/herdr/event" >/dev/null 2>&1 || true
