#!/bin/sh
# Speech fixtures for the voice-activity detector tests
# (Tests/SheepdogKitTests/VoiceActivityDetectorTests.swift), made with macOS
# text-to-speech: `say` renders each line to AIFF, `afconvert` turns it into
# the capture format the app sends — 16 kHz, mono, 16-bit little-endian WAV.
#
# The noise (white, pink, brown, babble, keyboard clicks) is generated in the
# test code itself; only the speech comes from here. The output is small
# (well under 2 MB) and committed, so CI does not need to run this — rerun it
# only to change the lines or the voices.
#
#   scripts/make-fixtures.sh [output-dir]
set -eu

out="${1:-$(cd "$(dirname "$0")/.." && pwd)/Tests/SheepdogKitTests/Audio}"
mkdir -p "$out"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# voice|file stem|line. Short, the way people talk to the dog.
while IFS='|' read -r voice stem line; do
    [ -z "$voice" ] && continue
    if ! say -v "$voice" -o "$tmp/$stem.aiff" "$line" 2>/dev/null; then
        echo "skip $stem: voice '$voice' is not installed" >&2
        continue
    fi
    afconvert -f WAVE -d LEI16@16000 -c 1 "$tmp/$stem.aiff" "$out/$stem.wav"
    echo "$out/$stem.wav"
done <<'LINES'
Samantha|speech-samantha|What is everyone doing right now?
Daniel|speech-daniel|Tell the auth agent to run the tests again.
Karen|speech-karen|Yes, go ahead and approve that.
Moira|speech-moira|Why did that one fail?
Fred|speech-fred|Stop. Wait a second, I need to check something.
Rishi|speech-rishi|Summarise what the docs agent changed today.
LINES
