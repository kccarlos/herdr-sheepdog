# Signing and notarizing the macOS binary

The macOS release binary (`darwin/arm64`) is signed with a **Developer ID
Application** certificate and notarized by Apple, so someone who downloads
it with a browser can run it without Gatekeeper blocking it. The Linux
build is not affected.

This is for the daemon's own macOS binary only. The **iOS companion app**
([`ios/`](../../ios/README.md)) is signed and shipped separately, by Xcode Cloud, not
by anything in this document — see
[`ios/docs/dev/xcode-cloud.md`](../../ios/docs/dev/xcode-cloud.md). As of 2026-09-28 this Mac's login keychain already
holds both certificates for team `GV776BZVA8`: the **Developer ID
Application** one this flow uses, and a separate **Apple Distribution**
one Xcode Cloud/Xcode use for the iOS app.

This happens **on the maintainer's Mac, not in GitHub Actions**:

1. Pushing a `v*` tag runs `.github/workflows/release.yml` on
   `ubuntu-latest`. It cross-builds both binaries with `CGO_ENABLED=0`,
   publishes the GitHub Release with both tarballs and `checksums.txt`,
   and adds a notice that the darwin asset is unsigned for now.
2. On the Mac, `make release-sign TAG=<tag>` (`scripts/release-sign.sh`)
   downloads the darwin tarball, checks it against `checksums.txt`, signs
   the binary with the Developer ID identity in the login keychain,
   notarizes it, re-packs the tarball under the same name, rewrites the
   darwin line of `checksums.txt`, and uploads both over the unsigned ones
   (`gh release upload --clobber`). It ends by asking Gatekeeper whether it
   now sees a notarized Developer ID binary.

**Why locally.** The repository is private, and GitHub bills macOS runner
minutes at ten times the Linux rate on private repositories. Signing needs
a Mac; this Mac already holds the certificate, so the release workflow
stays on Ubuntu and uses no macOS minutes. It also means the certificate's
private key never leaves the login keychain and there are no signing
secrets in GitHub.

Between steps 1 and 2 the release carries an unsigned darwin binary. It
runs fine from `curl`, `gh release download` or a terminal `tar -x`, and is
blocked by Gatekeeper only when downloaded with a browser (see
[When Gatekeeper actually checks](#when-gatekeeper-actually-checks)).

## One-time setup

### 1. Agreements

Signed in as the Account Holder (team
`GV776BZVA8`), accept any pending agreement at
<https://developer.apple.com/account>. Notarization fails with a 403
while one is pending.

### 2. The Developer ID Application certificate

It already exists on this Mac. To check:

```sh
security find-identity -v -p codesigning | grep "Developer ID Application"
#  4) <sha1> "Developer ID Application: <Account Holder name> (GV776BZVA8)"
```

The script picks the first valid identity whose name contains
"Developer ID Application" (override the match with
`SIGN_IDENTITY_MATCH`). If it is missing on a new Mac: Xcode ▸ Settings ▸
Accounts ▸ the paid team ▸ Manage Certificates… ▸ **+** ▸ Developer ID
Application (Account Holder only; a team can have only a few), or export
the `.p12` from the old Mac's Keychain Access and import it.

The first `codesign` with it shows a macOS prompt asking to let `codesign`
use the private key: enter the login password and choose **Always
Allow**. Until that is answered the script waits at "replacing existing
signature".

Note that the certificate's common name is the account holder's legal
name, so `codesign -dv` on the released binary shows
`Authority=Developer ID Application: <Account Holder name> (GV776BZVA8)`. Anyone can
read it from the download; that is how Developer ID works.

### 3. Notary credentials

`notarytool` reads its credentials from a keychain profile named
`sheepdog-notary` (override with `NOTARY_PROFILE`). Create it once with an
[app-specific password](https://support.apple.com/102654) for the Apple
ID (appleid.apple.com ▸ Sign-In and Security ▸ App-Specific Passwords):

```sh
xcrun notarytool store-credentials sheepdog-notary \
  --apple-id <Account Holder Apple ID> --team-id GV776BZVA8 \
  --password <app-specific password>
```

(Leave `--password` off to be prompted instead, which keeps it out of the
shell history.) notarytool validates the credentials against Apple and
stores them in the login keychain.

**Alternative: an App Store Connect API key.** Either store it in the
same profile:

```sh
xcrun notarytool store-credentials sheepdog-notary \
  --key ~/path/AuthKey_XXXXXXXXXX.p8 --key-id XXXXXXXXXX --issuer <issuer UUID>
```

or pass it per run with `NOTARY_KEY=<.p8 path> NOTARY_KEY_ID=… NOTARY_ISSUER=…`
(all three, which then take precedence over the profile). A key with the
Developer role is enough; the iOS app's key works too (see the iOS repo's
`docs/dev/signing.md`).

Check the profile with `xcrun notarytool history --keychain-profile
sheepdog-notary`.

## Per release

```sh
git tag v0.1.0
git push origin v0.1.0
gh run watch "$(gh run list --workflow release.yml --limit 1 --json databaseId -q '.[0].databaseId')"
make release-sign TAG=v0.1.0
```

`make release-sign` prints the signing identity, the `codesign --display`
summary (`flags=0x10000(runtime)`, `Authority=Developer ID Application:
…`, `TeamIdentifier=GV776BZVA8`, `Timestamp=`), the notarytool JSON with
`"status":"Accepted"` (a few minutes; it waits up to 30), the new
`checksums.txt`, and finally Gatekeeper's verdict with
`source=Notarized Developer ID`. If notarization is not `Accepted` it
prints Apple's log (`notarytool log <id>`) and stops before uploading.

- **Idempotent.** A second run downloads the already signed tarball,
  sees a Developer ID signature that Gatekeeper reports as notarized, and
  exits without changing anything. A run that stopped half-way can simply
  be repeated: the unsigned or signed-but-unnotarized binary is signed
  again from the start.
- **`make release-sign TAG=v0.1.0 DRY_RUN=1`** (`--dry-run`) downloads,
  verifies, signs and re-packs in a temp directory, but does not notarize
  or upload. Nothing leaves the Mac.

What the script does, in order: `gh release download <tag> -p
'sheepdog_*_darwin_arm64.tar.gz' -p checksums.txt` into a temp dir;
compare its sha256 with `checksums.txt`; check it holds exactly one
darwin/arm64 `sheepdog`; `codesign --options runtime --timestamp --force`
and `codesign --verify --strict`; zip it (`ditto -c -k --keepParent`, only
as the submission vehicle) and `xcrun notarytool submit --wait`; re-pack
with `tar` without macOS metadata; replace the darwin line of
`checksums.txt`; `gh release upload --clobber`; ask Gatekeeper.

**No entitlements.** The daemon is pure Go (`CGO_ENABLED=0`, no cgo, no
portaudio: audio runs in the browser). It opens sockets and spawns CLIs,
which the hardened runtime allows without entitlements.

**No stapling.** `xcrun stapler` works on `.app`, `.pkg` and `.dmg`, not
on a bare Mach-O binary. Gatekeeper looks the notarization ticket up
online the first time it evaluates the binary, so a user who is offline at
that moment may be refused until they are online.

## Checking a download

```sh
shasum -a 256 -c checksums.txt --ignore-missing
tar -xzf sheepdog_v0.1.0_darwin_arm64.tar.gz
codesign -dv --verbose=2 sheepdog     # Authority=Developer ID Application: <Account Holder name> (GV776BZVA8), flags=0x10000(runtime)
codesign --verify --strict --check-notarization -R=notarized -vv sheepdog
spctl --assess --type open --context context:primary-signature -vv sheepdog
# sheepdog: accepted
# source=Notarized Developer ID
```

`scripts/macos-sign.sh assess sheepdog` runs the last command.

Use `--type open --context context:primary-signature`, not `--type
execute`: for a bare command-line binary `spctl --assess --type execute`
answers `rejected (the code is valid but does not seem to be an app)`
whether or not it is notarized, and whether or not it carries a
quarantine attribute (checked against a notarized third-party CLI on
macOS 27). `spctl -t install` is for installer packages.

### When Gatekeeper actually checks

Gatekeeper only runs when a file carrying the `com.apple.quarantine`
extended attribute is first executed. A browser download gets it, and
Archive Utility passes it on to the extracted binary; that is the case
notarization is for. `curl`/`wget`, `gh release download`, `go install`
and local builds have no quarantine attribute, so Gatekeeper never looks
at them, signed or not. `spctl --assess` itself gives the same verdict
with or without the attribute. To see what a browser user would see,
add the attribute and run the binary:

```sh
xattr -w com.apple.quarantine "0081;$(printf %x "$(date +%s)");Safari;" sheepdog
./sheepdog version
```

An unsigned or unnotarized binary is then refused ("cannot be opened
because Apple cannot check it for malicious software"); a notarized one
runs.

## Troubleshooting

| Symptom | Cause |
| --- | --- |
| stuck at `replacing existing signature` | the keychain prompt for codesign is waiting on screen (see above) |
| `no valid 'Developer ID Application' identity in the keychain` | the certificate is missing, expired or revoked on this Mac |
| `No Keychain password item found for profile: sheepdog-notary` | the one-time `notarytool store-credentials` step has not been done |
| notarytool `HTTP status code: 401` | wrong app-specific password / API key, or it was revoked |
| notarytool `403` | an agreement is pending at developer.apple.com |
| notarization `Invalid` | the log printed by the script names the file and the issue (e.g. missing hardened runtime or timestamp) |
| `sha256 mismatch` | the release asset does not match its `checksums.txt`; do not sign it, rebuild the release |

## Testing without the real certificate

`SIGN_KEYCHAIN` points the scripts at another keychain, and
`SIGN_ALLOW_UNTRUSTED=1` accepts a self-signed test identity (local tests
only). `RELEASE_SIGN_LOCAL_DIR` reads the tarball and `checksums.txt` from
a directory instead of the release and never uploads;
`RELEASE_SIGN_KEEP` copies the re-packed files somewhere to inspect:

```sh
RELEASE_SIGN_LOCAL_DIR=/tmp/fake-release RELEASE_SIGN_KEEP=/tmp/out \
SIGN_KEYCHAIN=/tmp/test.keychain-db SIGN_ALLOW_UNTRUSTED=1 \
  scripts/release-sign.sh --dry-run v0.0.0-test
```
