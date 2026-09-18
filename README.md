# herdr-sheepdog

A voice supervisor for a herdr agent fleet, built on Gemini Live.

**Status:** pre-alpha. WP1–WP3 and WP5 of
[`sheepdog-v1-plan.md`](sheepdog-v1-plan.md) are in: the herdr socket
client, the debounced/deduplicated agent state watcher and the persisted
state table (`sheepdog herdr watch`), the Gemini Live voice session with
tool plumbing, push queue and resumption (`sheepdog voice-test`), the
supervisor with the five voice tools and the cheap-LLM templates, and the
daemon itself (`sheepdog serve`): JSONL event log, web server with the
`/debug` page, SSE tail, fake-event injection and the server side of the
browser WebSocket contract ([`docs/ws-contract.md`](docs/ws-contract.md)).
The real `/voice` page (WP6), the CLI query tool (WP4) and packaging (WP7)
are still to come.

## Usage (so far)

```sh
sheepdog serve                              # the daemon: http://127.0.0.1:7777/debug
sheepdog serve --voice=off --herdr=mock     # no Gemini, no herdr: drive it from /debug
sheepdog serve --verbose --port 8080        # echo every event to stderr

sheepdog herdr watch            # JSONL transitions on stdout, herdr.* events on stderr
sheepdog herdr watch --once     # print the persisted state table and exit

# stream a 16 kHz WAV through Gemini Live, write the 24 kHz reply, print events
sheepdog voice-test --in testdata/audio/question.wav --out reply.wav
sheepdog voice-test --text "yes go ahead"                       # simulated utterance
sheepdog voice-test --in q.wav --push "Auth is blocked" --push-after 9s --drop-after 15s
```

Configuration lives in `~/.config/sheepdog/config.toml` (optional) with
`SHEEPDOG_*` environment overrides; see `internal/config`. Keys so far:
`herdr.socket`, `state.path`, `data_dir`, `debounce_ms`,
`snapshot_interval_s`, `pending_ttl_s`, an `[aliases]` table mapping pane
ids to spoken names, e.g. `"w9:p1" = "auth"`, `[gemini]` with `api_keys`,
`model` and `voice`, `[llm]` with `base_url`, `model` and `api_key` (any
OpenAI-compatible endpoint; `http://localhost:11434/v1` for Ollama), and
`[web]` with `port` (default 7777, `SHEEPDOG_WEB_PORT`). The Gemini key pool is also read from `GEMINI_API_KEY` and
`GEMINI_API_KEY_1` … `GEMINI_API_KEY_9` (rotated on rate limits).

> **⚠️ Worker agents must NOT run in fully auto-approve mode.**
> Sheepdog's `propose()` sends instructions immediately after an echo, with
> no confirmation step — the worker agents' own approval prompts are the
> safety gate (plan decision D4). *TODO: expand this into a proper setup
> requirement once the supervisor exists (which coding-agent settings to
> check, how to verify, what happens if you don't).*

## Development

Requires Go (see `go.mod` for the version) and
[golangci-lint](https://golangci-lint.run/) v2.

```sh
cp .env.example .env   # fill in GEMINI_API_KEY etc.
make build              # -> bin/sheepdog
make test               # go test -race ./...
make vet                # go vet ./...
make lint               # golangci-lint run
make cross              # dist/sheepdog-darwin-arm64, dist/sheepdog-linux-amd64
make clean
```

See [`sheepdog-v1-plan.md`](sheepdog-v1-plan.md) for the full design and
work package breakdown.
