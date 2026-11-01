#!/usr/bin/env bash
# herdr-app.gif: the real curl installer and `sheepdog setup` followed by a
# private herdr-core + herdr-app window. The app starts on Herdr's main
# layout with one "shop" space, then the Sheepdog layout is switched in live.
#
#   scripts/readme-media/herdr-app.sh  # writes assets/readme/herdr-app.{gif,mp4}
#
# The terminal is a vhs take. The app is a screen recording of only the
# private window's 1440x900 logical-pixel region. A hidden curl shim serves
# this tree's scripts/install.sh and a release tarball built from this tree;
# the installer, setup, daemon, herdr-core and herdr-app are real. The
# daemon uses a loopback scripted brain, a state-only voice-ready proxy and
# voice=off, free ports, a placeholder key, a private HC_SOCKET/session and a
# throwaway HOME. Needs macOS Screen Recording permission for Ghostty,
# accessibility permission for resizing that private window, Go, herdr-core
# (hc), herdr-app, vhs, ffmpeg and gifsicle. No network access is needed.
set -euo pipefail

[ "$(uname -s)" = Darwin ] || { echo "herdr-app.sh records a macOS window" >&2; exit 1; }
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
# shellcheck source=scripts/readme-media/lib.sh
. "$here/lib.sh"
# A short path under /tmp keeps the private HOME and HC socket short.
work=${WORK:-$(mktemp -d /tmp/sda.XXXXXX)}
work=$(cd "$work" && pwd -P)
out=$repo/assets/readme
herdr_app=$(command -v herdr-app)
hc=$(command -v hc)

free_port() {
	python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()'
}
port=$(free_port)
[ "$port" != 7777 ] && [ "$port" != 7778 ] || port=$(free_port)
url=http://127.0.0.1:$port
brain_port=$(free_port)
[ "$brain_port" != "$port" ] || brain_port=$(free_port)
panel_port=$(free_port)
while [ "$panel_port" = "$port" ] || [ "$panel_port" = "$brain_port" ]; do
	panel_port=$(free_port)
done
panel_url=http://127.0.0.1:$panel_port

home=$work/h
app_home=$home/.config/herdr-app
state=$work/s
socket=$work/c.sock
bin=$home/bin
fake=$work/bin
release=$work/release
trigger=$work/block
mkdir -p "$home/shop" "$bin" "$fake" "$release" "$app_home" "$state" \
	"$home/.config/herdr" "$home/.config/herdr-app" \
	"$home/.config/sheepdog" "$work/data"

daemon_pid=
brain_pid=
state_proxy_pid=
app_pid=
recorder_pid=
cleanup() {
	set +e
	if [ -n "$recorder_pid" ]; then kill "$recorder_pid" 2>/dev/null; fi
	if [ -n "$app_pid" ]; then
		env HOME="$home" HERDR_APP_HOME="$app_home" HC_SOCKET="$socket" \
			HC_SESSION=sda XDG_STATE_HOME="$state" XDG_RUNTIME_DIR="$work" \
			PATH="$bin:/usr/bin:/bin" "$herdr_app" "do" app.quit >/dev/null 2>&1
		kill "$app_pid" 2>/dev/null
	fi
	if [ -n "$daemon_pid" ]; then kill "$daemon_pid" 2>/dev/null; fi
	if [ -n "$brain_pid" ]; then kill "$brain_pid" 2>/dev/null; fi
	if [ -n "$state_proxy_pid" ]; then kill "$state_proxy_pid" 2>/dev/null; fi
	if [ -x "$home/.local/bin/sheepdog" ]; then
		env HOME="$home" HC_SOCKET="$socket" HC_SESSION=sda \
			XDG_STATE_HOME="$state" XDG_RUNTIME_DIR="$work" \
			PATH="$home/.local/bin:$bin:/usr/bin:/bin" \
			"$home/.local/bin/sheepdog" stop >/dev/null 2>&1
	fi
	env HOME="$home" HC_SOCKET="$socket" HC_SESSION=sda \
		XDG_STATE_HOME="$state" XDG_RUNTIME_DIR="$work" \
		PATH="$bin:/usr/bin:/bin" "$hc" server stop >/dev/null 2>&1
	if [ "${KEEP_WORK:-}" != "1" ]; then rm -rf "$work"; fi
}
trap cleanup EXIT HUP INT TERM

app() {
	env HOME="$home" HERDR_APP_HOME="$app_home" HC_SOCKET="$socket" \
		HC_SESSION=sda XDG_STATE_HOME="$state" XDG_RUNTIME_DIR="$work" \
		HERDR_RELEASES_URL=http://127.0.0.1:9 \
		PATH="$home/.local/bin:$bin:/usr/bin:/bin" "$herdr_app" "$@"
}
run_hc() {
	env HOME="$home" HC_SOCKET="$socket" HC_SESSION=sda \
		XDG_STATE_HOME="$state" XDG_RUNTIME_DIR="$work" \
		PATH="$home/.local/bin:$bin:/usr/bin:/bin" "$hc" "$@"
}

# Build this tree the way the v0.1.19 release asset is packed.
release_version=v0.1.19
release_commit=$(git -C "$repo" rev-parse --short HEAD)
release_date=$(date -u +%Y-%m-%dT%H:%M:%SZ)
(cd "$repo" && go build -ldflags \
	"-X 'main.version=$release_version' -X 'main.commit=$release_commit' -X 'main.date=$release_date'" \
	-o "$fake/sheepdog" ./cmd/sheepdog)
asset=sheepdog_${release_version}_darwin_arm64.tar.gz
tar -czf "$release/$asset" -C "$fake" sheepdog
(cd "$release" && shasum -a 256 "$asset" >checksums.txt)

ln -s "$herdr_app" "$bin/herdr-app"
ln -s "$hc" "$bin/hc"

# Hidden curl: the exact public installer URL gets this tree's script; its
# release requests use the local file:// directory via SHEEPDOG_RELEASE_URL.
cat >"$bin/curl" <<EOF
#!/bin/sh
case "\$*" in
*raw.githubusercontent.com/kccarlos/herdr-sheepdog/main/scripts/install.sh*)
	cat "$repo/scripts/install.sh"
	;;
*)
	exec /usr/bin/curl "\$@"
	;;
esac
EOF
cat >"$bin/open" <<'EOF'
#!/bin/sh
exit 0
EOF
chmod +x "$bin/curl" "$bin/open"

cat >"$bin/tests" <<EOF
#!/bin/sh
set -eu
report() { printf '\033]7501;%s\033\\' "\$1"; }
message() { printf '%s' "\$1" | base64 | tr -d '\n'; }
printf '\033]0;Shop tests\033\\'
printf '\nShop checks\n\n'
report 'state=idle:app=tests:title=Shop tests'
printf '  ✓ 42 checks passed in 1.8s\n\n'
while [ ! -f "$trigger" ]; do sleep 0.1; done
printf '  Do you want to run the end-to-end tests?\n\n'
printf '  ❯ 1. Yes, run them\n'
printf '    2. Not now\n\n'
report "state=blocked:kind=permission:app=tests:msg=\$(message 'run the end-to-end tests')"
while :; do sleep 30; done
EOF
chmod +x "$bin/tests"

printf "PS1='$ '\nexport PATH='%s:%s:/usr/bin:/bin'\n" "$bin" "$home/.local/bin" \
	>"$home/.zshrc"
printf 'onboarding = false\n' >"$home/.config/herdr/config.toml"
cat >"$home/.config/sheepdog/config.toml" <<EOF
data_dir = "$work/data"

[herdr]
backend = "hc"
hc_socket = "$socket"

[brain]
provider = "fixture"
model = "scripted"

[[brain.openai_compat]]
name = "fixture"
label = "README fixture"
base_url = "http://127.0.0.1:$brain_port/v1"
model = "scripted"

[web]
bind = "127.0.0.1"
port = $port
advertise = ["192.168.1.20:$port"]
EOF
printf 'GEMINI_API_KEY=your-ai-studio-key\nSHEEPDOG_HERDR_BACKEND=hc\n' \
	>"$home/.config/sheepdog/.env"
chmod 600 "$home/.config/sheepdog/.env"
app_version=$(env HOME="$home" HERDR_APP_HOME="$app_home" \
	HC_SOCKET="$socket" HC_SESSION=sda XDG_STATE_HOME="$state" \
	XDG_RUNTIME_DIR="$work" PATH="$bin:/usr/bin:/bin" \
	"$herdr_app" --version | awk '{print $2}')
printf '{"last_run":"%s","presented":[]}\n' "$app_version" \
	>"$app_home/update-notes.json"

# Write the private daemon URL and skip the first-run welcome before the app
# has ever opened a panel. setup preserves these shared values.
cat >"$app_home/shared.json" <<EOF
{"frame.welcomed":"skipped","layout":"main","sheepdog.url":"$panel_url"}
EOF

run_hc server start >/dev/null
env HOME="$home" HERDR_APP_HOME="$app_home" HC_SOCKET="$socket" \
	HC_SESSION=sda XDG_STATE_HOME="$state" XDG_RUNTIME_DIR="$work" \
	HERDR_RELEASES_URL=http://127.0.0.1:9 \
	PATH="$home/.local/bin:$bin:/usr/bin:/bin" "$herdr_app" \
	>"$work/app.log" 2>&1 &
app_pid=$!
for _ in $(seq 1 40); do
	app status >/dev/null 2>&1 && break
	sleep 0.25
done
app status >/dev/null
app "do" overlay.close >/dev/null 2>&1 || true
app "do" app.theme --name atom-one-dark >/dev/null
space=$(app "do" space.new --name shop --folder "$home/shop")
terminal=$(printf '%s' "$space" | jq -r .terminal)
[ -n "$terminal" ] && [ "$terminal" != null ] || { echo "shop space has no terminal" >&2; exit 1; }
cat >>"$home/.config/sheepdog/config.toml" <<EOF

[aliases]
"$terminal" = "shop-tests"
EOF
run_hc send "$terminal" tests

for _ in $(seq 1 40); do
	run_hc get "$terminal" | jq -e '.report.state == "idle" or .status == "idle"' >/dev/null 2>&1 && break
	sleep 0.25
done

# Only the private app's window: 1440x900 logical at a fixed position.
window_position='60, 60'
window_size='1440, 900'
resize_window() {
	osascript -e 'tell application "System Events"' \
		-e "set p to first process whose unix id is $app_pid" \
		-e 'set frontmost of p to true' \
		-e 'tell p' \
		-e "set position of window 1 to {$window_position}" \
		-e "set size of window 1 to {$window_size}" \
		-e 'end tell' -e 'end tell'
}
resize_window
cliclick m:1,1
sleep 0.8
app shot "$work/main-before.png" >/dev/null

# Use the same private config with the no-network scripted brain. Starting it
# before setup keeps setup from following the layout switch with daemon
# startup and browser output inside the terminal take.
env -i PATH="$PATH" python3 "$here/herdr-app-brain.py" "$brain_port" \
	>"$work/brain.log" 2>&1 &
brain_pid=$!
for _ in $(seq 1 40); do
	curl -fsS "http://127.0.0.1:$brain_port/v1/models" >/dev/null 2>&1 && break
	sleep 0.1
done
curl -fsS "http://127.0.0.1:$brain_port/v1/models" >/dev/null
env -i HOME="$home" HC_SOCKET="$socket" HC_SESSION=sda \
	XDG_STATE_HOME="$state" XDG_RUNTIME_DIR="$work" \
	PATH="$home/.local/bin:$bin:/usr/bin:/bin" \
	"$fake/sheepdog" serve --voice=off \
	>"$work/daemon.log" 2>&1 &
daemon_pid=$!
for _ in $(seq 1 80); do
	curl -fsS "$url/api/state" >"$work/state.json" 2>/dev/null && break
	sleep 0.25
done
curl -fsS "$url/api/state" >"$work/state.json"
env -i PATH="$PATH" python3 "$here/herdr-app-state.py" "$port" "$panel_port" \
	>"$work/state-proxy.log" 2>&1 &
state_proxy_pid=$!
for _ in $(seq 1 40); do
	curl -fsS "$panel_url/api/state" >"$work/panel-state.json" 2>/dev/null && break
	sleep 0.1
done
jq -e '.voice.mode == "gemini" and .voice.status == "ready"' \
	"$work/panel-state.json" >/dev/null

# Pass 1: the terminal. Dynamic waits make the actual installer and setup
# output, rather than fixed sleeps, decide when each visible step happened.
cp "$here/herdr-app.tape" "$work/"
(cd "$work" && env -i HOME="$HOME" PATH="$PATH" TERM=xterm-256color \
	SDHOME="$home" SDPATH="$home/.local/bin:$bin:/usr/bin:/bin" \
	SDRELEASE="file://$release" SDAPP="$app_home" SDHC_SOCKET="$socket" \
	SDHC_STATE="$state" SDWORK="$work" vhs herdr-app.tape)

# Pass 2: the same fixture starts on the unaffected Herdr main layout,
# then receives the same real layout.switch that setup ran in pass 1.
app "do" layout.switch --name main >/dev/null
for _ in $(seq 1 40); do
	app status | jq -e '.layout == "main"' >/dev/null 2>&1 && break
	sleep 0.2
done
app status | jq -e '.layout == "main"' >/dev/null
app "do" agent.focus --terminal "$terminal" >/dev/null
resize_window
cliclick m:1,1
cliclick kp:esc
sleep 0.8
app shot "$work/app-main.png" >/dev/null

rm -f "$trigger"
# AVFoundation provides the screen; the crop emits only the private app's
# window region. The source is 2x at 1440x1000 logical pixels.
ffmpeg -loglevel error -y -f avfoundation -capture_cursor 0 -framerate 15 \
	-i '0:none' -t 12 -vf 'crop=2880:1800:120:120' -an \
	-c:v libx264 -preset ultrafast -crf 18 -pix_fmt yuv420p \
	"$work/window.mov" &
recorder_pid=$!
sleep 1.3
app "do" layout.switch --name sheepdog >/dev/null
for _ in $(seq 1 40); do
	app status | jq -e '.layout == "sheepdog"' >/dev/null 2>&1 && break
	sleep 0.2
done
app "do" layout.resize --id sheepdog --px 720 >/dev/null
sleep 1.2
: >"$trigger"
for _ in $(seq 1 80); do
	curl -fsS "$url/api/state" >"$work/state.json" 2>/dev/null &&
		jq -e '.pending_action != null and .pending_action.question != ""' \
			"$work/state.json" >/dev/null 2>&1 && break
	sleep 0.25
done
jq -e '.pending_action != null and .pending_action.question != ""' \
	"$work/state.json" >/dev/null
jq -e '.pending_action.agent == "shop-tests"' "$work/state.json" >/dev/null
sleep 2

# At this height the panel deliberately uses its compact "Answer" bar.
# Open the real question sheet so both choices are visible, then return to
# the conversation for the typed turn.
cliclick c:1445,229
sleep 1.2
app shot "$work/app-question.png" >/dev/null
# Close the question sheet through its own Close button.
cliclick c:1432,304
sleep 0.5

curl -fsS -m 20 -H 'Content-Type: application/json' \
	-d '{"text":"How are my agents doing?"}' "$url/api/turn" \
	>"$work/turn.json"
jq -e '.reply != ""' "$work/turn.json" >/dev/null
jq -e '.reply == "shop-tests is waiting on you: it asks to run the end-to-end tests."' \
	"$work/turn.json" >/dev/null
sleep 2.2
wait "$recorder_pid"
recorder_pid=
window_duration=$(ffprobe -v error -show_entries format=duration -of csv=p=0 "$work/window.mov")
python3 -c 'import sys; assert float(sys.argv[1]) >= 10, sys.argv[1]' "$window_duration"
cliclick m:1,1
sleep 0.4
app shot "$work/app-after.png" >/dev/null

# The terminal take is compressed only if needed to keep the whole story
# under 20 seconds. Both halves stay real-time relative to themselves.
term=$work/herdr-app.mp4
term_duration=$(ffprobe -v error -show_entries format=duration -of csv=p=0 "$term")
term_keep=$(python3 -c "import sys; print(min(float(sys.argv[1]), 8.1))" "$term_duration")
term_speed=$(python3 -c "import sys; d=float(sys.argv[1]); k=float(sys.argv[2]); print(max(1.0, d/k))" "$term_duration" "$term_keep")
app_duration=$(ffprobe -v error -show_entries format=duration -of csv=p=0 "$work/window.mov")
app_keep=$(python3 -c "import sys; print(min(float(sys.argv[1]), 11.8))" "$app_duration")

mkdir -p "$out"
ffmpeg -loglevel error -y -i "$term" -i "$work/window.mov" \
	-filter_complex \
	"[0:v]trim=duration=$term_keep,setpts=(PTS-STARTPTS)/$term_speed,fps=30,scale=800:500:flags=lanczos,setsar=1[t];\
[1:v]trim=duration=$app_keep,setpts=PTS-STARTPTS,fps=30,scale=800:500:flags=lanczos,setsar=1[a];\
[t][a]concat=n=2:v=1:a=0,format=yuv420p[v]" \
	-map '[v]' -c:v libx264 -crf 23 -movflags +faststart -an \
	"$out/herdr-app.mp4"
to_gif "$out/herdr-app.mp4" "$out/herdr-app.gif" 800 10

size=$(stat -f%z "$out/herdr-app.gif")
if [ "$size" -gt 1572864 ] && command -v gifsicle >/dev/null; then
	for loss in 60 100 160 220; do
		gifsicle -O3 --lossy="$loss" --batch "$out/herdr-app.gif"
		size=$(stat -f%z "$out/herdr-app.gif")
		[ "$size" -le 1572864 ] && break
	done
fi
if [ "$size" -gt 1572864 ]; then
	echo "herdr-app.gif is $((size / 1024)) KB; want at most 1.5 MB" >&2
	exit 1
fi
echo "work files in $work"
