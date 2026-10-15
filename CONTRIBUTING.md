# Contributing

Thanks for helping. Bug reports, setup problems and ideas are as useful as code.

## Report a problem

Open an [issue](https://github.com/kccarlos/herdr-sheepdog/issues/new/choose) and pick a template. For a bug or a setup problem, attach the output of

```sh
sheepdog doctor --report
```

It redacts keys and tokens and shows your home directory as `~`; read it before you post it. For a security issue, do not open an issue: see [SECURITY.md](SECURITY.md).

## Set up a development checkout

You need Go (the version in `go.mod`), [golangci-lint](https://golangci-lint.run/) v2, and Node (only for the browser tests).

```sh
git clone https://github.com/kccarlos/herdr-sheepdog.git && cd herdr-sheepdog
make build        # bin/sheepdog
make run-dev      # a daemon with no voice, no herdr and no key; poke at /debug
make test         # go test -race ./... (includes the docs checks)
make test-web     # browser-side tests (needs node)
make vet lint
make eval         # offline eval sets, no network, no keys
```

Architecture first: [docs/dev/architecture.md](docs/dev/architecture.md). The developer guide is [docs/dev/development.md](docs/dev/development.md); the vocabulary is [docs/dev/glossary.md](docs/dev/glossary.md).

Never commit a key, a token, a `.env` file or a log that has not been through `sheepdog doctor --report`. Tests must not touch your real `~/.config/sheepdog`; `cmd/sheepdog`'s tests already point `HOME` at a temp directory.

## Changes

- **One change per PR**, with a test that fails without it. Prompt and harness changes also need the eval evidence described in [docs/dev/eval/README.md](docs/dev/eval/README.md); several ideas that looked good lost there.
- **Docs travel with the code.** A user-visible change updates the matching page in [docs/](docs/README.md); a new config key needs its entry in [CONFIG.md](CONFIG.md) (`go test` fails otherwise). Every relative link and every `sheepdog …` command in the user docs is checked by `go test ./internal/docscheck`.
- **Conventional Commits** for every commit and PR title: `feat(scope): …`, `fix(scope): …`, `docs: …`, `test:`, `refactor:`, `ci:`, `build:`, `perf:`, `chore:`. The changelog is generated from them.
- **Pull requests** target `main`, use the template, and merge by squash once CI is green. Describe what a user sees, with the exact output when it changed.
- **CI** runs vet, race tests, lint, web tests, the offline eval and cross-builds on Linux only. Anything Xcode or iOS lives in the [iOS repository](https://github.com/kccarlos/herdr-sheepdog-ios).

## License

By contributing you agree that your work is released under the [MIT License](LICENSE).
