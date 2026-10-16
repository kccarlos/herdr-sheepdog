# Contributing

Thanks for helping. Bug reports, setup problems and ideas are as useful as
code.

## Report a problem

Open an [issue](https://github.com/kccarlos/herdr-sheepdog-ios/issues/new).
Include:

- the app version (Settings > About) and the iOS version;
- what you did, what you expected and what the app said (a screenshot helps);
- for a connection problem, the output of `sheepdog doctor --report` on the
  computer. It redacts keys and tokens. Read it before you post it.

Problems with the daemon itself belong in the
[main repository](https://github.com/kccarlos/herdr-sheepdog/issues). For a
security issue, do not open an issue; see [SECURITY.md](SECURITY.md).

## Set up a development checkout

You need a Mac with Xcode 27 and [XcodeGen](https://github.com/yonaskolb/XcodeGen)
2.46 or newer.

```sh
git clone https://github.com/kccarlos/herdr-sheepdog-ios.git && cd herdr-sheepdog-ios
xcodegen generate
xcodebuild -scheme Sheepdog -destination 'platform=iOS Simulator,name=iPhone 17 Pro' test
```

To build onto a device under your own Apple team, see "Build it yourself"
in the [README](README.md#build-it-yourself). The UI tests that need a
daemon skip themselves without one; to run them, start the fixture daemons
described in [docs/dev/status.md](docs/dev/status.md#running-the-tests).
Read [docs/dev/architecture.md](docs/dev/architecture.md) before you change
code.

## Changes

- **One change per pull request**, with a test that fails without it. Logic
  goes in `Sources/SheepdogKit` with a unit test. A flow the user sees gets
  an XCUITest, and it should run without a daemon when it can (see
  `Tests/SheepdogUITests/StubDaemon.swift`).
- **Design the unhappy paths.** A new screen needs its empty state and its
  error states with a plain fix, and it must work in dark mode, at the
  largest Dynamic Type size and on a small iPhone.
- **Conventional Commits** for every commit and PR title: `feat(scope): …`,
  `fix(scope): …`, `docs: …`, `test: …`, `refactor: …`, `ci: …`, `chore: …`.
- **CI is Xcode Cloud.** Every pull request runs the tests there. Do not
  add GitHub Actions macOS jobs. Pull requests merge by squash once the
  Xcode Cloud run is green.
- **Docs travel with the code.** User-facing behaviour goes in the
  [README](README.md). Developer notes go in [docs/dev/](docs/dev/).
- Never commit a token, a push key, a `.env` file or a real name, phone
  number or private address.

## License

By contributing you agree that your work is released under the
[MIT License](LICENSE).
