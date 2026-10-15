# Homebrew packaging (draft)

Nothing here is published yet. The formula goes to the existing tap
`kccarlos/homebrew-tap` once the repository is public and a release is
tagged.

- `sheepdog.rb.tmpl`: the formula, with placeholders for the version and
  the two sha256 sums (darwin/arm64, linux/amd64; the targets release.yml
  builds today).
- `render.sh <tag>`: fills the template from the release's
  `checksums.txt` and prints the formula.
- `../../.goreleaser.yaml`: a draft goreleaser config that builds the same
  assets as release.yml; not used by CI yet.

Release order, when the time comes (each step by the maintainer):

1. Tag `vX.Y.Z`; the release workflow builds the tarballs and
   `checksums.txt`.
2. `make release-sign TAG=vX.Y.Z` on the Mac signs and notarizes the darwin
   binary and replaces its tarball and checksum (docs/dev/signing.md).
3. `packaging/homebrew/render.sh vX.Y.Z > <tap checkout>/Formula/sheepdog.rb`,
   then review, commit and push in the tap.
4. `brew install kccarlos/tap/sheepdog` on a clean machine.

The formula's checksum must come from step 2's checksums, not step 1's:
signing changes the darwin tarball.
