# Homebrew packaging

The formula is published to the tap `kccarlos/homebrew-tap` as
`Formula/sheepdog.rb` by the `homebrew` job of `.github/workflows/release.yml`,
after the darwin binary is signed and notarized (docs/dev/signing.md):
`brew install kccarlos/tap/sheepdog`.

- `sheepdog.rb.tmpl`: the formula, with placeholders for the tag and the two
  sha256 sums (darwin/arm64, linux/amd64; the targets release.yml builds).
  Homebrew reads the version from the URL.
- `render.sh <tag>`: fills the template from the release's `checksums.txt`
  and prints the formula.
- `tap-update.sh <tap checkout> <formula> <tag>`: writes
  `Formula/sheepdog.rb`, adds a row to the tap README's table once, and
  commits `sheepdog <version>` as KC; it does not push.
- `tap-update_test.sh`: tests both against a scratch repository (CI runs it).
- `../../.goreleaser.yaml`: a draft goreleaser config that builds the same
  assets as release.yml; not used by CI.

By hand, when Actions cannot (after `make release-sign TAG=vX.Y.Z`):

```sh
git clone https://github.com/kccarlos/homebrew-tap /tmp/tap
packaging/homebrew/render.sh vX.Y.Z > /tmp/sheepdog.rb
packaging/homebrew/tap-update.sh /tmp/tap /tmp/sheepdog.rb vX.Y.Z
git -C /tmp/tap push origin HEAD:main
```

The formula's checksum must come from the signed release's checksums:
signing changes the darwin tarball.
