# Instructions for AI agents

You are working on **Sheepdog for iOS**, the SwiftUI phone app for the
[sheepdog](https://github.com/kccarlos/herdr-sheepdog) daemon. Read this
file, then [docs/dev/architecture.md](docs/dev/architecture.md) before you
change code. [CONTRIBUTING.md](CONTRIBUTING.md) is the human version of
these rules.

- **Generate the project** with `xcodegen generate`; `Sheepdog.xcodeproj` is
  not committed. Settings, Info.plist keys and entitlements live in
  `project.yml`.
- **Run the tests** on a simulator before you say a change works, and show
  the output:
  `xcodebuild -scheme Sheepdog -destination 'platform=iOS Simulator,name=iPhone 17 Pro' test`.
  UI tests that need a fixture daemon skip without one
  ([docs/dev/status.md](docs/dev/status.md#running-the-tests)). Prefer
  tests that need no daemon (`StubDaemon`, a closed port, the offline demo
  launched with `-sheepdogDemoSilent`, since audio hangs on Xcode Cloud).
- **CI is Xcode Cloud only.** Never add GitHub Actions macOS jobs
  ([docs/dev/xcode-cloud.md](docs/dev/xcode-cloud.md)).
- **First-time users come first.** Every screen needs its empty and error
  states with a plain fix, VoiceOver labels, Dynamic Type without
  truncation and a dark-mode check. Do not promise what a self-hosted
  daemon cannot do (push notifications need the app team's APNs key).
- **Never commit secrets** (tokens, `.p8` keys, `.env`) and never use real
  names, phone numbers, personal e-mail addresses or private network
  addresses in code, docs or fixtures.
- **Conventional Commits** for commits and PR titles (`feat(settings): …`,
  `fix(audio): …`, `docs: …`). Do not add session links or co-author
  trailers.
- **Docs:** the README is for users; developer docs are in
  [docs/dev/](docs/dev/). The daemon's user docs (pairing, remote access,
  privacy) live in the daemon repository; link to them instead of copying.
