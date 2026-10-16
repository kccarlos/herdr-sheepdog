#!/bin/sh
# Xcode Cloud runs this after cloning, from the ci_scripts directory, before
# it opens the project. The .xcodeproj is not committed (XcodeGen generates
# it from project.yml), so this script makes it exist.
# https://developer.apple.com/documentation/xcode/writing-custom-build-scripts
# https://developer.apple.com/documentation/xcode/making-dependencies-available-to-xcode-cloud
set -e

XCODEGEN_VERSION=2.46.0
XCODEGEN_SHA256=4d9e34b62172d645eed6457cac13fc222569974098ef4ee9c3368bedf0196806

cd "${CI_PRIMARY_REPOSITORY_PATH:?CI_PRIMARY_REPOSITORY_PATH is not set (this script is meant for Xcode Cloud)}"

# A tag build ships to TestFlight with MARKETING_VERSION from project.yml;
# stop early if the tag says something else (v0.1.0 needs 0.1.0).
if [ -n "$CI_TAG" ]; then
    version=$(sed -n 's/^ *MARKETING_VERSION: *"\{0,1\}\([^"]*\)"\{0,1\} *$/\1/p' project.yml | head -n 1)
    if [ "$CI_TAG" != "v$version" ]; then
        echo "error: tag $CI_TAG does not match MARKETING_VERSION $version in project.yml; bump it and tag v$version" >&2
        exit 1
    fi
    echo "Tag $CI_TAG matches MARKETING_VERSION $version"
fi

# The build number: Xcode Cloud assigns CI_BUILD_NUMBER to every build and
# App Store Connect uses it for the uploaded build, so nothing is rewritten.
echo "Xcode Cloud build ${CI_BUILD_NUMBER:-(none)} for ${CI_XCODEBUILD_ACTION:-an unknown action}"

# XcodeGen: the pinned release binary first (seconds, and the same version
# every time), Homebrew if the download fails.
if command -v xcodegen >/dev/null 2>&1; then
    xcodegen=$(command -v xcodegen)
else
    dir="${TMPDIR:-/tmp}/xcodegen-$XCODEGEN_VERSION"
    zip="$dir.zip"
    xcodegen="$dir/xcodegen/bin/xcodegen"
    if curl -fsSL --retry 3 -o "$zip" \
        "https://github.com/yonaskolb/XcodeGen/releases/download/$XCODEGEN_VERSION/xcodegen.zip" &&
        echo "$XCODEGEN_SHA256  $zip" | shasum -a 256 -c - &&
        rm -rf "$dir" && unzip -q "$zip" -d "$dir"; then
        echo "Using XcodeGen $XCODEGEN_VERSION from the release zip"
    else
        echo "warning: the XcodeGen $XCODEGEN_VERSION download failed; installing with Homebrew" >&2
        HOMEBREW_NO_AUTO_UPDATE=1 HOMEBREW_NO_INSTALL_CLEANUP=1 brew install xcodegen
        xcodegen=$(command -v xcodegen)
    fi
fi

"$xcodegen" --version
"$xcodegen" generate --spec project.yml

if [ ! -f Sheepdog.xcodeproj/xcshareddata/xcschemes/Sheepdog.xcscheme ]; then
    echo "error: xcodegen did not write the shared Sheepdog scheme" >&2
    exit 1
fi
