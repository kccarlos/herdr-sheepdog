#!/bin/sh
# XcodeGen's postGenCommand (project.yml). Copies the committed
# Package.resolved into the generated project, where Xcode and xcodebuild look
# for it, so every build resolves the same package versions. Xcode Cloud
# needs it: it does not resolve packages on its own and fails with "a
# resolved file is required when automatic dependency resolution is
# disabled" when the file is missing.
#
# To update a pin: change the version in project.yml, run
# `scripts/install-package-resolved.sh --update` (it deletes the copy, lets
# xcodebuild resolve again and saves the result next to project.yml), then
# commit both files. docs/dev/architecture.md "The terminal surface".
set -e

ios=$(cd "$(dirname "$0")/.." && pwd)
committed="$ios/Package.resolved"
generated="$ios/Sheepdog.xcodeproj/project.xcworkspace/xcshareddata/swiftpm/Package.resolved"

if [ "$1" = "--update" ]; then
    rm -f "$generated"
    (cd "$ios" && xcodebuild -resolvePackageDependencies -project Sheepdog.xcodeproj -scheme Sheepdog >/dev/null)
    cp "$generated" "$committed"
    echo "Saved $committed"
    exit 0
fi

if [ ! -f "$committed" ]; then
    echo "error: $committed is missing; run scripts/install-package-resolved.sh --update" >&2
    exit 1
fi
mkdir -p "$(dirname "$generated")"
cp "$committed" "$generated"
