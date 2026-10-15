// Package web implements the daemon's HTTP surface (plan §3.8), bound to
// 127.0.0.1 only in v1:
//
//   - GET /voice and GET /debug: the embedded static pages (root web/).
//   - GET /events: an SSE tail of the event log (`event: log`, one JSON
//     line per event, `id:` = the event's stable id). Last-Event-ID or
//     ?since=<id> replays what came after that id from the log (across
//     day files and restarts, at most 2 000 events), ?n=<count> the last
//     count events of the ring; ?source= / ?pane= / ?type= filter; a
//     keepalive comment every 15 s.
//   - GET /api/state, GET /api/agents, GET /api/events?n=: the state
//     table, pending action (with its options for a tap), session and
//     voice status as JSON.
//   - GET /api/conversation?limit=&before=: the shepherd/dog conversation
//     rebuilt from the log across days and restarts, newest last, paged
//     backwards by event id (conversation.go).
//   - POST /api/pending/answer {id, option}: answer the open question by
//     tap, a user input through the one rail.
//   - POST /api/pending/cancel, POST /api/inject/{blocked,done,state,
//     screen,remove,utterance}: the debug page's controls, all routed
//     through the same supervisor paths real events take
//     (internal/inject). /api/inject/screen replaces a synthetic pane's
//     text without moving its state or touching the supervisor — the
//     mock counterpart of a worker showing a new prompt with no herdr
//     status change in between, for exercising the post-answer poll and
//     the reconciler (docs/dev/roles.md "blocked -> blocked is a block").
//   - POST /api/herdr/event: an optional second way into the Watcher
//     besides the herdr socket subscription (another process or a test
//     can feed it): the body is a herdr event object in the
//     HERDR_PLUGIN_EVENT_JSON shape and status events are fed to the
//     Watcher through Inject.
//   - GET /ws: the browser audio pipe of docs/dev/ws-contract.md, server side.
//     Binary frames go to the voice session, JSON control messages are
//     routed to the session or the supervisor, model audio and events come
//     back as frames. One client at a time; a bounded outbound queue drops
//     the oldest audio when the browser cannot keep up.
//
// The page is a viewer of the log (decision D10): nothing here is a second
// source of truth, and every action the page can take produces the same
// events the real path would.
package web
