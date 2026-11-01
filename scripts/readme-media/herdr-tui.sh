#!/usr/bin/env bash
# herdr-tui.gif: the real GitHub plugin install, then herdr's "Pair your
# phone" pane in a throwaway HOME.
#
#   scripts/readme-media/herdr-tui.sh  # writes assets/readme/herdr-tui.{gif,mp4}
#
# The plugin command is the one a user types. It clones the public repo and
# downloads the latest release. Every process runs in a private HOME,
# herdr-app home, herdr-core session and XDG directories; the sheepdog
# daemon uses a free port and advertises only the example address
# 192.168.1.20. Needs herdr, herdr-core (hc), Go, vhs, ffmpeg and network
# access (gifsicle optional).
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
# shellcheck source=scripts/readme-media/lib.sh
. "$here/lib.sh"
work=${WORK:-$(mktemp -d -t sheepdog-herdr-tui)}
out=$repo/assets/readme
herdr=$(command -v herdr)
hc=$(command -v hc)

free_port() {
	python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()'
}
port=$(free_port)
[ "$port" != 7777 ] && [ "$port" != 7778 ] || port=$(free_port)

# A short HOME: both herdr and herdr-core use unix socket paths.
home=$(mktemp -d "${TMPDIR:-/tmp}/sdt.XXXX")
home=$(cd "$home" && pwd)
app_home=$home/herdr-app
state=$home/state
socket=$home/c.sock
run_hc() {
	env HOME="$home" HC_SOCKET="$socket" HC_SESSION=sdtui \
		XDG_STATE_HOME="$state" XDG_RUNTIME_DIR="$home" \
		PATH="$home/bin:/usr/bin:/bin" "$hc" "$@"
}
cleanup() {
	set +e
	if [ -x "$home/.local/bin/sheepdog" ]; then
		env HOME="$home" HC_SOCKET="$socket" HC_SESSION=sdtui \
			XDG_STATE_HOME="$state" XDG_RUNTIME_DIR="$home" \
			PATH="$home/.local/bin:$home/bin:/usr/bin:/bin" \
			"$home/.local/bin/sheepdog" stop >/dev/null 2>&1
	fi
	if [ -f "$home/data/sheepdog.pid" ]; then
		kill "$(cat "$home/data/sheepdog.pid")" 2>/dev/null
	fi
	env -i HOME="$home" XDG_CONFIG_HOME="$home/.config" HERDR_APP_HOME="$app_home" \
		PATH=/usr/bin:/bin "$herdr" server stop >/dev/null 2>&1
	run_hc server stop >/dev/null 2>&1
	rm -rf "$home"
	if [ "${KEEP_WORK:-}" != "1" ]; then rm -rf "$work"; fi
}
trap cleanup EXIT HUP INT TERM

mkdir -p "$home/calc" "$home/bin" "$home/.config/herdr" \
	"$home/.config/sheepdog" "$app_home" "$state" "$work"
ln -s "$herdr" "$home/bin/herdr"
ln -s "$hc" "$home/bin/hc"
printf 'onboarding = false\n' >"$home/.config/herdr/config.toml"
printf 'data_dir = "%s/data"\n[herdr]\nbackend = "hc"\nhc_socket = "%s"\n[web]\nbind = "127.0.0.1"\nport = %s\nadvertise = ["192.168.1.20:%s"]\n' \
	"$home" "$socket" "$port" "$port" >"$home/.config/sheepdog/config.toml"
printf 'GEMINI_API_KEY=your-ai-studio-key\nSHEEPDOG_HERDR_BACKEND=hc\n' \
	>"$home/.config/sheepdog/.env"
chmod 600 "$home/.config/sheepdog/.env"
for rc in .bashrc .bash_profile; do
	printf "PS1='\$ '\nexport BASH_SILENCE_DEPRECATION_WARNING=1\n" >"$home/$rc"
done

run_hc server start >/dev/null
cp "$here/herdr-tui.tape" "$work/"
(cd "$work" && env -i HOME="$HOME" PATH="$PATH" TERM=xterm-256color \
	SDHOME="$home" SDPATH="$home/.local/bin:$home/bin:/usr/bin:/bin" \
	SDAPP="$app_home" SDHC_SOCKET="$socket" SDHC_STATE="$state" \
	SDHERDR="$herdr" \
	vhs herdr-tui.tape)

src=$work/herdr-tui.mp4
duration=$(ffprobe -v error -show_entries format=duration -of csv=p=0 "$src")
keep=$(python3 -c "import sys; print(min(float(sys.argv[1]), 15.0))" "$duration")
speed=$(python3 -c "import sys; d=float(sys.argv[1]); k=float(sys.argv[2]); print(max(1.0, d/k))" "$duration" "$keep")

mkdir -p "$out"
ffmpeg -loglevel error -y -i "$src" -t "$keep" \
	-vf "setpts=PTS/$speed,fps=30" -c:v libx264 -pix_fmt yuv420p \
	-crf 24 -movflags +faststart -an "$out/herdr-tui.mp4"
to_gif "$out/herdr-tui.mp4" "$out/herdr-tui.gif" 800 12

# The QR animation and terminal glyphs are detailed enough that some takes
# need lossy palette compression to stay below the README's 1 MB budget.
size=$(stat -f%z "$out/herdr-tui.gif")
if [ "$size" -gt 1048576 ] && command -v gifsicle >/dev/null; then
	for loss in 60 100 160; do
		gifsicle -O3 --lossy="$loss" --batch "$out/herdr-tui.gif"
		size=$(stat -f%z "$out/herdr-tui.gif")
		[ "$size" -le 1048576 ] && break
	done
fi
if [ "$size" -gt 1048576 ]; then
	echo "herdr-tui.gif is $((size / 1024)) KB; want at most 1 MB" >&2
	exit 1
fi
echo "work files in $work"
