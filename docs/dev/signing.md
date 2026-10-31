# Signing and notarizing the macOS binary

The macOS release binary (`darwin/arm64`) is signed with a **Developer ID
Application** certificate and notarized by Apple, so someone who downloads
it with a browser can run it without Gatekeeper blocking it. The Linux
build is not affected.

This is for the daemon's own macOS binary and, since the same job signs
it, the Mac app ([Sheepdog.app](#sheepdogapp)). The **iOS companion app**
([`ios/`](../../ios/README.md)) is signed and shipped separately, by Xcode Cloud (which only archives
TestFlight builds; the app's tests run in `ios.yml` on GitHub Actions), not
by anything in this document — see
[`ios/docs/dev/xcode-cloud.md`](../../ios/docs/dev/xcode-cloud.md). As of 2026-09-28 this Mac's login keychain already
holds both certificates for team `GV776BZVA8`: the **Developer ID
Application** one this flow uses, and a separate **Apple Distribution**
one Xcode Cloud/Xcode use for the iOS app.

## The release flow

Signing runs in GitHub Actions, in `.github/workflows/release.yml`, since
the repository went public (macOS runner minutes are free on public
repositories). Pushing a `v*` tag runs three jobs:

```
release (Cloud Run, ubuntu)      build darwin/arm64 + linux/amd64, create the GitHub Release
   │                              (darwin tarball unsigned for now)
   ▼  ── the maintainer approves the `release` environment ──
sign-macos (GitHub-hosted xcode-27, environment: release)
   │                              temporary keychain ← MACOS_CERT_P12_BASE64
   │                              scripts/release-sign.sh <tag>: codesign, notarytool, re-tar,
   │                              rewrite checksums.txt, gh release upload --clobber
   │                              then download the published tarball and verify it again
   │                              scripts/release-sign-app.sh <tag>: Sheepdog.app (see below),
   │                              Sheepdog-<version>-macos.zip added to the release and checksums.txt
   ▼
homebrew (GitHub-hosted ubuntu-latest, environment: release)
                                  render.sh (formula; --cask when the app signed) → ruby -c →
                                  brew audit --strict → commit Formula/sheepdog.rb, and
                                  Casks/sheepdog-app.rb when the app signed, to
                                  kccarlos/homebrew-tap, push →
                                  brew install kccarlos/tap/sheepdog (Linux smoke test)
```

1. **release** cross-builds both binaries with `CGO_ENABLED=0` and
   publishes the Release with both tarballs and `checksums.txt`.
2. **sign-macos** waits for the maintainer to approve the `release`
   environment (Actions ▸ the run ▸ Review deployments). It imports the
   Developer ID certificate into a throwaway keychain with a random
   password, writes the App Store Connect API key to `$RUNNER_TEMP` (mode
   0600), and runs `scripts/release-sign.sh <tag>`, the same script as the
   local fallback below: download, check the sha256, `codesign --options
   runtime --timestamp`, zip and `notarytool submit --wait` (on anything
   but `Accepted` it prints Apple's log and fails before uploading),
   re-pack under the same name, replace the darwin line of
   `checksums.txt` (the linux line stays), `gh release upload --clobber`.
   It then downloads the published tarball and checks it with `codesign
   --verify --strict` and Gatekeeper (`spctl --assess --type open
   --context context:primary-signature`, the check that works for a bare
   CLI binary; see [Checking a download](#checking-a-download)). The
   keychain and the key file are deleted in an always-run step.
   After the daemon, the same job signs [Sheepdog.app](#sheepdogapp). Those
   app steps are allowed to fail: the job records `app_signed=false`, warns
   in the annotation and job summary that the app was not shipped, and still
   succeeds so the daemon can be released.
3. **homebrew** runs whenever the daemon signing succeeded. It renders
   `Formula/sheepdog.rb` from the signed `checksums.txt` with
   `packaging/homebrew/render.sh`, checks it with `ruby -c` and
   `brew audit --strict`, and has `packaging/homebrew/tap-update.sh` commit
   it to `kccarlos/homebrew-tap` as `sheepdog <version>` (author KC), adding
   a README table row the first time. When `app_signed` is true it also
   renders the cask with `render.sh --cask`, syntax-checks it (casks need
   macOS, so it has no audit in this job), and passes it to `tap-update.sh`
   in the same commit, adding its README row the first time. A formula-only
   update leaves the existing cask untouched. A rerun with unchanged files
   commits nothing. It ends by installing the formula from the tap on Linux
   and running `brew test`.

Both sign-macos and homebrew run in the `release` environment, so a run
asks for approval twice: once before signing, and again when homebrew
becomes pending after signing succeeds.

If `OP_SERVICE_ACCOUNT_TOKEN` is missing from the environment,
sign-macos skips with a notice and the darwin tarball stays unsigned;
homebrew runs only after a successful signing, so the tap never points at
an unsigned binary. The tap token comes from the same 1Password vault.

The `policy` CI check (`scripts/workflow-policy.sh`) allows a macOS runner
and these secrets in `release.yml` only inside a job that declares
`environment: release` (see [development.md](development.md#cicd)).

### The `release` environment

Settings ▸ Environments ▸ `release`: required reviewer the maintainer,
deployment policy "tags matching `v*`". Its one secret is
`OP_SERVICE_ACCOUNT_TOKEN`, a 1Password service account token that can
read only the vault `github`; the jobs install the 1Password CLI
(`1password/install-cli-action`) and load everything else into step outputs
with `scripts/op-load.sh`, which retries each `op read` up to four times with
growing sleeps (1Password is sometimes flaky) and masks every value. It is an **environment**
secret, not a repository one, so only jobs that pass the approval can read it.

| 1Password item (vault `github`) | Fields | Workflow name |
| --- | --- | --- |
| `apple-developer-id-application` | `p12_base64`, `password` | `MACOS_CERT_P12_BASE64`, `MACOS_CERT_PASSWORD`: the Developer ID Application `.p12` (certificate and private key) for team `GV776BZVA8` |
| `app-store-connect-api-key` | `p8_base64`, `key_id` | `NOTARY_API_KEY_P8`, `NOTARY_KEY_ID`: the App Store Connect API key (base64 of the `.p8`) and its Key ID |
| `homebrew-tap-token` | `credential` | `HOMEBREW_TAP_TOKEN`: fine-grained PAT, repository `kccarlos/homebrew-tap` only, Contents read and write |

The issuer ID (`686fbf99-b25d-4e9c-af7c-2b7cb2c3f1eb`) is not secret and is
set in the workflow as `NOTARY_ISSUER_ID`.

Setting the token (from 1Password, never the command line):

```sh
op read "op://Private/OP service account github-actions/credential" \
  | gh secret set OP_SERVICE_ACCOUNT_TOKEN --env release
```

Export the identity (certificate with its private key) as the `.p12`;
the job also imports Apple's Developer ID G2 intermediate, so the chain
need not be in the file. Delete the local `.p12` and `.p8` copies after
storing them in 1Password.

### Rotating

- **Certificate** (expires after five years, or if revoked): create a new
  Developer ID Application certificate (see [the local
  setup](#2-the-developer-id-application-certificate)), export it, and
  update both fields of the 1Password item `apple-developer-id-application`.
  Binaries signed with the old certificate keep working unless it is
  revoked.
- **API key**: create a new key in App Store Connect ▸ Users and Access ▸
  Integrations, update the 1Password item `app-store-connect-api-key`, then
  revoke the old key.
- **Tap token**: fine-grained PATs expire; create a new one with the same
  scope, update `homebrew-tap-token` in vault `github` (and
  `GITHUB_PAT_HOMEBREW` in `Developer`, the same token), delete the old one.

The next run picks them up; nothing in the repository changes.

### Re-signing an existing release

A `workflow_dispatch` with the `tag` input re-runs sign-macos and homebrew
for a release that already exists, without rebuilding it. The run must be
dispatched on the tag's own ref so the environment's tag policy admits
it:

```sh
gh workflow run release.yml --ref v0.1.1 -f tag=v0.1.1
```

`release-sign.sh` is idempotent: an already signed and notarized tarball
is left alone. The homebrew job republishes the formula, and the cask when
the app is signed and verified.

GitHub runs the workflow file **as it is at the dispatched ref**. Tags
created before this workflow had `workflow_dispatch` (v0.1.0) cannot be
dispatched on their own ref: GitHub answers that the workflow has no
`workflow_dispatch` trigger. For those, either add a deployment branch
rule for `main` to the `release` environment and dispatch from `main`:

```sh
gh workflow run release.yml --ref main -f tag=v0.1.0
```

(remove the rule afterwards if only tags should deploy), or sign on the
Mac with `make release-sign TAG=v0.1.0` and push the formula by hand
(`packaging/homebrew/README.md`).

## Local fallback: `make release-sign`

The same signing runs on the maintainer's Mac when Actions cannot, using
the Developer ID identity in the login keychain and a notarytool keychain
profile:

1. `make release-sign TAG=<tag>` (`scripts/release-sign.sh`) downloads
   the darwin tarball, checks it against `checksums.txt`, signs, notarizes,
   re-packs, rewrites the darwin line of `checksums.txt`, uploads both
   over the unsigned ones (`gh release upload --clobber`), and ends by
   asking Gatekeeper whether it now sees a notarized Developer ID binary.

Between the release job and signing, the release carries an unsigned
darwin binary. It runs fine from `curl`, `gh release download` or a
terminal `tar -x`, and is blocked by Gatekeeper only when downloaded with
a browser (see
[When Gatekeeper actually checks](#when-gatekeeper-actually-checks)).

### One-time setup

#### 1. Agreements

Signed in as the Account Holder (team
`GV776BZVA8`), accept any pending agreement at
<https://developer.apple.com/account>. Notarization fails with a 403
while one is pending.

#### 2. The Developer ID Application certificate

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

#### 3. Notary credentials

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

### Per release (local)

```sh
git tag v0.1.0
git push origin v0.1.0
gh run watch "$(gh run list --workflow release.yml --limit 1 --json databaseId -q '.[0].databaseId')"
make release-sign TAG=v0.1.0
```

The tag push also starts the Actions flow above; reject its environment
approval when signing locally. `make release-sign` prints the signing identity, the `codesign --display`
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

## Sheepdog.app

The Mac app (`ios/`, scheme `SheepdogMac`, product `Sheepdog.app`, bundle id
`com.github.kccarlos.herdr-sheepdog-mac`; [design](design/desktop-client.md))
ships as `Sheepdog-<version>-macos.zip` on the same release as the daemon,
with its sha256 in `checksums.txt`. It uses the **same** Developer ID
Application identity, the same temporary keychain and the same App Store
Connect API key as the daemon, so no new secret exists. Sign-macos runs
`scripts/release-sign-app.sh <tag>` after the daemon:

1. If `checksums.txt` already lists the zip, stop (a rerun changes nothing;
   to redo the app, delete the asset and its line).
2. Select Xcode 27.1 on the `xcode-27` image (the runner and Xcode `ios.yml`
   tests with), install the pinned XcodeGen (the version `ios.yml` and Xcode
   Cloud use), `xcodegen generate`.
3. `xcodebuild archive` of `SheepdogMac`, Release, arm64 only (like the
   daemon), unsigned (`CODE_SIGNING_ALLOWED=NO`): the project's automatic
   signing is for Debug and needs the team's development certificate.
   `MARKETING_VERSION` is the tag without `v` and without a pre-release suffix
   (`v1.2.3-rc1` is 1.2.3); the script checks the built
   `CFBundleShortVersionString`.
4. Sign inside-out with `scripts/macos-sign.sh`'s `msign_sign`: nested
   frameworks first, then the app with `SIGN_ENTITLEMENTS` set to
   `ios/Sources/SheepdogMac/SheepdogMac.entitlements`, `codesign --options
   runtime --timestamp`. The script checks the signed app carries the
   microphone entitlement.
5. `notarytool submit --wait` on a `ditto` zip (`msign_notarize`, which prints
   Apple's log and fails on anything but `Accepted`), then `xcrun stapler
   staple` and `stapler validate` on the `.app` itself.
6. `ditto -c -k --keepParent` the stapled app into the zip, append its line
   to `checksums.txt` (the daemon and linux lines stay), `gh release upload
   --clobber` both.
7. The workflow downloads the zip again, checks its sha256, `codesign
   --verify --deep --strict`, the team id, `stapler validate` and `spctl
   --assess --type execute` (for an `.app`, unlike the bare daemon, this
   reports `source=Notarized Developer ID`).

Unlike the daemon, the app has entitlements (only
`com.apple.security.device.audio-input`, for the voice session) and it is
stapled, so it opens offline. The bundle id and team never change: the
microphone grant is tied to them.

**Debug and local builds need no release identity.** `xcodebuild` from the
generated project signs Debug automatically with the team's development
certificate, or run it with `CODE_SIGNING_ALLOWED=NO` for a build that only
has to compile. `scripts/release-sign-app.sh --dry-run <tag>` builds and
signs but neither notarizes nor uploads; it needs a Developer ID identity in
the keychain (`SIGN_KEYCHAIN` and `SIGN_ALLOW_UNTRUSTED=1` accept a
self-signed one, as in [Testing without the real
certificate](#testing-without-the-real-certificate)).

### The cask

`brew install --cask kccarlos/tap/sheepdog-app` installs the zip into
`/Applications` (`packaging/homebrew/sheepdog-app.rb.tmpl`: `app
"Sheepdog.app"`, arm64, macOS 14 or newer, `zap` for the app's
preferences). The token is `sheepdog-app` because the daemon's formula is
already `sheepdog`. Its `livecheck` follows `v*` tags only, so the iOS app's
`ios-v*` tags do not count as a new version.

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
| sign-macos: `no valid Developer ID Application identity` | the 1Password item `apple-developer-id-application` holds the certificate without its private key, the wrong certificate, or an expired one |
| sign-macos: `security import` fails | the item's `password` does not match its `.p12` |
| sign-macos skipped with a notice | `OP_SERVICE_ACCOUNT_TOKEN` is not set in the `release` environment |
| dispatch refused by the environment | the run was not dispatched on a `v*` tag ref |
| sign-macos: the app step fails at `xcodebuild archive` | the job's Xcode is not the one `ios.yml` uses (v0.1.15 ran on macos-15, whose newest Xcode, 26.3, cannot build `SheepdogKitMac`); keep `runs-on` and the selected Xcode in step with `ios.yml`; the log shows `xcodebuild -version` |
| `Sheepdog.app not shipped` warning | an app-specific step failed; the daemon release and its Homebrew formula continue. Fix the app and re-dispatch to add or update the cask |
| `Sheepdog.app cask not published` warning | the app signed but the cask could not be rendered; the daemon formula still publishes. Fix the cask and re-dispatch |
| homebrew: push rejected (403) | `homebrew-tap-token` expired or lacks Contents write on the tap |
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
