# Homebrew packaging

The formula and the cask are published to the tap `kccarlos/homebrew-tap` as
`Formula/sheepdog.rb` and `Casks/sheepdog-app.rb` by the `homebrew` job of
`.github/workflows/release.yml`, after the darwin binary and Sheepdog.app are
signed and notarized (docs/dev/signing.md):
`brew install kccarlos/tap/sheepdog` and
`brew install --cask kccarlos/tap/sheepdog-app`. The cask's token differs
from the formula's because both live in one tap.

- `sheepdog.rb.tmpl`: the formula, with placeholders for the tag and the two
  sha256 sums (darwin/arm64, linux/amd64; the targets release.yml builds).
  Homebrew reads the version from the URL.
- `sheepdog-app.rb.tmpl`: the cask (`app "Sheepdog.app"`, macOS 14 or newer,
  arm64, `zap` for the app's preferences), with placeholders for the tag, the
  version and the zip's sha256.
- `render.sh [--cask] <tag>`: fills a template from the release's
  `checksums.txt` and prints the formula, or with `--cask` the cask.
- `tap-update.sh <tap checkout> <formula> <cask> <tag>`: writes
  `Formula/sheepdog.rb` and `Casks/sheepdog-app.rb`, adds a row for each to
  the tap README's table once, and commits both as `sheepdog <version>` as
  KC; it does not push. It fails, writing nothing, if only one of the two
  differs from the tap.
- `tap-update_test.sh`: tests both against a scratch repository (CI runs it).
- `../../.goreleaser.yaml`: a draft goreleaser config that builds the same
  assets as release.yml; not used by CI.

By hand, when Actions cannot (after `make release-sign TAG=vX.Y.Z` and
`scripts/release-sign-app.sh vX.Y.Z`):

```sh
git clone https://github.com/kccarlos/homebrew-tap /tmp/tap
packaging/homebrew/render.sh vX.Y.Z > /tmp/sheepdog.rb
packaging/homebrew/render.sh --cask vX.Y.Z > /tmp/sheepdog-app.rb
packaging/homebrew/tap-update.sh /tmp/tap /tmp/sheepdog.rb /tmp/sheepdog-app.rb vX.Y.Z
git -C /tmp/tap push origin HEAD:main
```

The checksums must come from the signed release's checksums: signing
changes the darwin tarball, and the app's zip is added after signing.
