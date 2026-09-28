#!/usr/bin/env bash
# Signs, notarizes and checks a bare macOS binary with a Developer ID
# Application certificate that is already in a keychain on this Mac (the
# login keychain by default). scripts/release-sign.sh sources this file for
# `make release-sign`; it also runs on its own. See docs/signing.md.
#
#   scripts/macos-sign.sh sign <binary>      # codesign (hardened runtime) + verify
#   scripts/macos-sign.sh notarize <binary>  # zip + notarytool submit --wait
#   scripts/macos-sign.sh assess <binary>    # Gatekeeper verdict (Notarized Developer ID?)
#
# Environment:
#   SIGN_IDENTITY_MATCH    substring the identity must contain
#                          (default "Developer ID Application")
#   SIGN_KEYCHAIN          keychain to look in (default: the user's search
#                          list, i.e. the login keychain)
#   SIGN_ALLOW_UNTRUSTED=1 also accept identities that do not chain to a
#                          trusted root (local tests with a self-signed
#                          certificate only)
#   NOTARY_PROFILE         notarytool keychain profile (default sheepdog-notary),
#                          created once with `xcrun notarytool store-credentials`
#   NOTARY_KEY, NOTARY_KEY_ID, NOTARY_ISSUER
#                          instead of the profile: an App Store Connect API
#                          key (.p8 path), its key ID and issuer ID
#
# Nothing here prints or exports a private key or a password.

set -euo pipefail

msign_die() { echo "macos-sign: $*" >&2; exit 1; }

# Prints "<sha1> <name>" of the first matching code-signing identity.
msign_find_identity() {
  local match="${SIGN_IDENTITY_MATCH:-Developer ID Application}"
  local -a flags=(-v -p codesigning)
  local untrusted=0
  [[ "${SIGN_ALLOW_UNTRUSTED:-}" == 1 ]] && { flags=(-p codesigning); untrusted=1; }
  # `-v` still lists revoked certificates (with a CSSMERR_ note); skip them.
  security find-identity "${flags[@]}" ${SIGN_KEYCHAIN:+"$SIGN_KEYCHAIN"} \
    | awk -v m="$match" -v u="$untrusted" 'index($0, m) && (u || !/CSSMERR_/) && $2 ~ /^[0-9A-F]{40}$/ {
        name = $0; sub(/^[^"]*"/, "", name); sub(/".*$/, "", name)
        print $2, name; exit }'
}

# True when <binary> carries a valid signature from an identity matching
# SIGN_IDENTITY_MATCH (used to skip re-signing an already signed binary).
msign_is_signed() {
  local bin="$1" match="${SIGN_IDENTITY_MATCH:-Developer ID Application}"
  codesign --verify --strict "$bin" 2>/dev/null || return 1
  codesign --display --verbose=2 "$bin" 2>&1 | grep -q "^Authority=${match}"
}

msign_sign() {
  local bin="${1:?usage: sign <binary>}"
  [[ -f "$bin" ]] || msign_die "no such file: $bin"

  local found hash name
  found="$(msign_find_identity)"
  if [[ -z "$found" ]]; then
    security find-identity -v -p codesigning ${SIGN_KEYCHAIN:+"$SIGN_KEYCHAIN"} >&2 || true
    msign_die "no valid '${SIGN_IDENTITY_MATCH:-Developer ID Application}' identity in the keychain"
  fi
  hash="${found%% *}"
  name="${found#* }"
  echo "macos-sign: signing with \"$name\""
  [[ -z "${SIGN_KEYCHAIN:-}" ]] && echo "macos-sign: (the first time, macOS asks to let codesign use the key: enter the login password, then Always Allow)"

  # Hardened runtime (required for notarization) and a secure timestamp.
  # The binary is pure Go (CGO_ENABLED=0), so it needs no entitlements.
  local -a kc=()
  [[ -n "${SIGN_KEYCHAIN:-}" ]] && kc=(--keychain "$SIGN_KEYCHAIN")
  codesign --sign "$hash" ${kc[@]+"${kc[@]}"} --options runtime --timestamp --force "$bin"

  codesign --verify --strict --verbose=2 "$bin"
  codesign --display --verbose=2 "$bin" 2>&1 | grep -E '^(Identifier|Authority|TeamIdentifier|Timestamp|CodeDirectory)' || true
}

# notarytool authentication: an API key if NOTARY_KEY* are set, otherwise
# the keychain profile.
msign_notary_auth() {
  if [[ -n "${NOTARY_KEY:-}" || -n "${NOTARY_KEY_ID:-}" || -n "${NOTARY_ISSUER:-}" ]]; then
    [[ -n "${NOTARY_KEY:-}" && -n "${NOTARY_KEY_ID:-}" && -n "${NOTARY_ISSUER:-}" ]] \
      || msign_die "set all of NOTARY_KEY, NOTARY_KEY_ID and NOTARY_ISSUER, or none"
    [[ -f "$NOTARY_KEY" ]] || msign_die "NOTARY_KEY: no such file: $NOTARY_KEY"
    MSIGN_AUTH=(--key "$NOTARY_KEY" --key-id "$NOTARY_KEY_ID" --issuer "$NOTARY_ISSUER")
  else
    MSIGN_AUTH=(--keychain-profile "${NOTARY_PROFILE:-sheepdog-notary}")
  fi
}

msign_notarize() {
  local bin="${1:?usage: notarize <binary>}"
  [[ -f "$bin" ]] || msign_die "no such file: $bin"
  msign_notary_auth

  local work zip out status id
  work="$(mktemp -d "${TMPDIR:-/tmp}/notarize.XXXXXX")"
  # notarytool takes zip, pkg or dmg; a bare binary goes in a zip. The zip
  # is only the submission vehicle: the release ships the signed binary.
  zip="$work/$(basename "$bin").zip"
  ditto -c -k --keepParent "$bin" "$zip"

  out="$work/submit.json"
  echo "macos-sign: submitting to Apple's notary service (usually a few minutes)"
  if ! xcrun notarytool submit "$zip" "${MSIGN_AUTH[@]}" --wait --timeout 30m --output-format json > "$out"; then
    echo "macos-sign: notarytool submit exited non-zero" >&2
  fi
  cat "$out"; echo

  status="$(plutil -extract status raw -o - "$out" 2>/dev/null || true)"
  id="$(plutil -extract id raw -o - "$out" 2>/dev/null || true)"
  rm -rf "$work"
  if [[ "$status" != "Accepted" ]]; then
    if [[ -n "$id" ]]; then
      echo "macos-sign: notarization log for $id:" >&2
      xcrun notarytool log "$id" "${MSIGN_AUTH[@]}" >&2 || true
    fi
    msign_die "notarization status is '${status:-unknown}', expected 'Accepted'"
  fi
  echo "macos-sign: notarization accepted ($id)"
}

# Asks Gatekeeper about <binary> and prints its verdict; a notarized one
# prints "source=Notarized Developer ID". Returns spctl's exit status.
#
# `spctl --assess --type execute` answers "rejected (the code is valid but
# does not seem to be an app)" for every bare Mach-O tool, notarized or not
# and quarantined or not (checked against a notarized third-party CLI on
# macOS 27), so it cannot tell. `--type open --context
# context:primary-signature` evaluates the binary's own signature and does
# report the notarization; the quarantine attribute does not change its
# answer, so no quarantined copy is needed.
msign_assess() {
  local bin="${1:?usage: assess <binary>}"
  [[ -f "$bin" ]] || msign_die "no such file: $bin"
  spctl --assess --type open --context context:primary-signature -vv "$bin" 2>&1
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  case "${1:-}" in
    sign)     shift; msign_sign "$@" ;;
    notarize) shift; msign_notarize "$@" ;;
    assess)   shift; msign_assess "$@" ;;
    *) msign_die "usage: $0 sign <binary> | notarize <binary> | assess <binary>" ;;
  esac
fi
