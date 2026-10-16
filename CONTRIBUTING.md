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
- **CI** runs vet, race tests, lint, web tests, the offline eval and cross-builds on Linux only. A PR that changes only `ios/` skips those steps; the iPhone app builds on Xcode Cloud (see below).

## The iPhone app (ios/)

The SwiftUI app lives in [ios/](ios/README.md). For an app problem, include the app version (Settings > About) and the iOS version in the issue, plus `sheepdog doctor --report` for a connection problem.

You need a Mac with Xcode 27 and [XcodeGen](https://github.com/yonaskolb/XcodeGen) 2.46 or newer:

```sh
cd ios
xcodegen generate
xcodebuild -scheme Sheepdog -destination 'platform=iOS Simulator,name=iPhone 17 Pro' test
```

To build onto a device under your own Apple team, see [ios/README.md](ios/README.md#build-it-yourself). The UI tests that need a daemon skip themselves without one; to run them, start the fixture daemons described in [ios/docs/dev/status.md](ios/docs/dev/status.md#running-the-tests). Read [ios/docs/dev/architecture.md](ios/docs/dev/architecture.md) before you change the app.

- Logic goes in `ios/Sources/SheepdogKit` with a unit test. A flow the user sees gets an XCUITest, and it should run without a daemon when it can (see `ios/Tests/SheepdogUITests/StubDaemon.swift`).
- **Design the unhappy paths.** A new screen needs its empty state and its error states with a plain fix, and it must work in dark mode, at the largest Dynamic Type size and on a small iPhone.
- **iOS CI.** Every PR that touches `ios/` runs the app's tests on Xcode Cloud ([ios/docs/dev/xcode-cloud.md](ios/docs/dev/xcode-cloud.md)) and the unit tests on a GitHub-hosted Mac (`.github/workflows/ios.yml`, no secrets). The only other macOS job is `release.yml`'s signing job, in the approval-gated `release` environment ([docs/dev/signing.md](docs/dev/signing.md)); do not add macOS jobs anywhere else, the `policy` check fails if one does.
- App releases are `ios-v*` tags (`ios-v` + `MARKETING_VERSION`); plain `v*` tags are daemon releases: the tag builds the release, and after the maintainer approves the `release` environment, Actions signs and notarizes the macOS binary and publishes the Homebrew formula ([docs/dev/signing.md](docs/dev/signing.md)).

## License

By contributing you agree that your work is released under the [MIT License](LICENSE).
