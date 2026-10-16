# Instructions for AI agents

You are working on **sheepdog**, a Go daemon that lets a person manage coding agents running in herdr by voice, and its iPhone app in [ios/](ios/README.md). Read this file, then [docs/dev/architecture.md](docs/dev/architecture.md) before you change code. [CONTRIBUTING.md](CONTRIBUTING.md) is the human version of the rules below.

- **Words:** use the terms in [docs/dev/glossary.md](docs/dev/glossary.md) (user, voice, orchestrator, agents/workers).
- **Run the tests** before you say a change works: `make test` (Go, race detector, includes the docs checks), `make test-web`, `make vet lint`, and `make eval` for prompt or orchestrator changes. Show the output.
- **Never commit secrets.** No API keys, tokens, `.env` files, push keys or unredacted logs. Do not print them either. Tests must not read or write the real `~/.config/sheepdog` or `~/.local/share/sheepdog`, and must not touch a running daemon.
- **Conventional Commits** for commits and PR titles (`feat(cli): …`, `fix(web): …`, `docs: …`). Do not add your own session links or co-author trailers.
- **Docs:** user docs are in [docs/](docs/README.md); developer docs are in [docs/dev/](docs/dev/). Each fact lives in one place. Keep relative links working and every `sheepdog …` command in the user docs real: `go test ./internal/docscheck` checks both. New config keys go in [CONFIG.md](CONFIG.md).
- **Prompt and harness changes** need eval evidence: read [docs/dev/eval/README.md](docs/dev/eval/README.md) first, because most obvious ideas were already measured.
- **Safety rails stay in code.** Do not move the approval rule or any other rail into a prompt.
- Do not use real names, phone numbers or private addresses in code, docs or fixtures.

## The iPhone app (ios/)

The SwiftUI app in `ios/` follows the rules above, plus these. Read [ios/docs/dev/architecture.md](ios/docs/dev/architecture.md) before you change it; run the commands below from `ios/`.

- **Generate the project** with `xcodegen generate`; `Sheepdog.xcodeproj` is not committed. Settings, Info.plist keys and entitlements live in `ios/project.yml`.
- **Run the tests** on a simulator before you say a change works, and show the output: `xcodebuild -scheme Sheepdog -destination 'platform=iOS Simulator,name=iPhone 17 Pro' test`. UI tests that need a fixture daemon skip without one ([ios/docs/dev/status.md](ios/docs/dev/status.md#running-the-tests)). Prefer tests that need no daemon (`StubDaemon`, a closed port, the offline demo launched with `-sheepdogDemoSilent`, since audio hangs on Xcode Cloud).
- **iOS CI is Xcode Cloud only.** Never add GitHub Actions macOS jobs ([ios/docs/dev/xcode-cloud.md](ios/docs/dev/xcode-cloud.md)). App releases are `ios-v*` tags; `v*` tags are daemon releases.
- **First-time users come first.** Every screen needs its empty and error states with a plain fix, VoiceOver labels, Dynamic Type without truncation and a dark-mode check. Do not promise what a self-hosted daemon cannot do (push notifications need the app team's APNs key).
- **Docs:** the app's user docs are [docs/iphone-app.md](docs/iphone-app.md) and [docs/privacy.md](docs/privacy.md); [ios/README.md](ios/README.md) is about building it, and its developer notes are in [ios/docs/dev/](ios/docs/dev/).
