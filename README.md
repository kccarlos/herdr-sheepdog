# herdr-sheepdog

A voice supervisor for a herdr agent fleet, built on Gemini Live.

**Status:** pre-alpha / bootstrap. Nothing here does anything yet; this is
the repository skeleton (module, package layout, CI/release plumbing) ahead
of the work packages in [`sheepdog-v1-plan.md`](sheepdog-v1-plan.md).

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
