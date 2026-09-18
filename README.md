# herdr-sheepdog

A voice supervisor for a herdr agent fleet, built on Gemini Live.

**Status:** pre-alpha. WP1 and WP2 of
[`sheepdog-v1-plan.md`](sheepdog-v1-plan.md) are in: the herdr socket
client, the debounced/deduplicated agent state watcher and the persisted
state table (`sheepdog herdr watch`), and the Gemini Live voice session
with tool plumbing, push queue and resumption (`sheepdog voice-test`,
echo tools only). No supervisor or web layer yet; the browser ↔ daemon
WebSocket contract is in [`docs/ws-contract.md`](docs/ws-contract.md).

## Usage (so far)

```sh
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
`snapshot_interval_s`, an `[aliases]` table mapping pane ids to spoken
names, e.g. `"w9:p1" = "auth"`, and `[gemini]` with `api_keys`, `model`
and `voice`. The Gemini key pool is also read from `GEMINI_API_KEY` and
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
