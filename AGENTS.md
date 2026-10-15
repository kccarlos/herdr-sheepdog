# Instructions for AI agents

You are working on **sheepdog**, a Go daemon that lets a person manage coding agents running in herdr by voice. Read this file, then [docs/dev/architecture.md](docs/dev/architecture.md) before you change code. [CONTRIBUTING.md](CONTRIBUTING.md) is the human version of the rules below.

- **Words:** use the terms in [docs/dev/glossary.md](docs/dev/glossary.md) (user, voice, orchestrator, agents/workers).
- **Run the tests** before you say a change works: `make test` (Go, race detector, includes the docs checks), `make test-web`, `make vet lint`, and `make eval` for prompt or orchestrator changes. Show the output.
- **Never commit secrets.** No API keys, tokens, `.env` files, push keys or unredacted logs. Do not print them either. Tests must not read or write the real `~/.config/sheepdog` or `~/.local/share/sheepdog`, and must not touch a running daemon.
- **Conventional Commits** for commits and PR titles (`feat(cli): …`, `fix(web): …`, `docs: …`). Do not add your own session links or co-author trailers.
- **Docs:** user docs are in [docs/](docs/README.md); developer docs are in [docs/dev/](docs/dev/). Each fact lives in one place. Keep relative links working and every `sheepdog …` command in the user docs real: `go test ./internal/docscheck` checks both. New config keys go in [CONFIG.md](CONFIG.md).
- **Prompt and harness changes** need eval evidence: read [docs/dev/eval/README.md](docs/dev/eval/README.md) first, because most obvious ideas were already measured.
- **Safety rails stay in code.** Do not move the approval rule or any other rail into a prompt.
- Do not use real names, phone numbers or private addresses in code, docs or fixtures.
