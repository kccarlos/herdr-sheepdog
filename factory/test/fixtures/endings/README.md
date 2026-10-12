The last lines of saved agent conversations, as test input (`test/retry.test.js`; the fake herdr replays them).

Recorded from real files on 2026-10-12 (Codex 0.160.0, Claude Code 2.1.296, pi 1.1.0), with long text cut, and
signatures, request ids and folder names taken out:

- `codex-at-capacity`: the review of herdr-superwork/37 that this was built for. One opening message, one command,
  then `task_complete` with no last message and the error "Selected model is at capacity" (`server_overloaded`).
- `codex-unauthorized`, `codex-model-not-supported`: `task_complete` with a 401 and with a 400 (`other`).
- `pi-socket-closed`, `pi-overloaded`, `pi-gateway-502`, `pi-stream-ended`, `pi-unknown-provider`, `pi-flagged`:
  a message with `stopReason: "error"` and its `errorMessage`.
- `claude-model-not-found`: an `isApiErrorMessage` entry (`model_not_found`, 404).

Not recorded, because none could be found or provoked: the files named `-made` are written by hand in the shape of
a recorded one. `claude-overloaded-made` and `claude-rate-limit-made` have the recorded Claude entry's shape with
the API's documented 529 and 429 texts; the two `-finished-made` files are an ordinary finished turn.
