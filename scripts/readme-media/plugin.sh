#!/usr/bin/env bash
# plugin.gif: a vhs recording of herdr with sheepdog linked as a plugin
# (plugin.tape): `herdr plugin link`, then the "Pair your phone" popup.
#
#   scripts/readme-media/plugin.sh          # writes assets/readme/plugin.{gif,mp4}
#
# herdr runs inside the vhs terminal with a throwaway HOME and
# XDG_CONFIG_HOME and a clean environment (env -i), so its config, plugin
# registry, sessions and server socket are new and thrown away; the real
# herdr (and a herdr this script may run inside) is never reached. The
# sheepdog config is a dummy: the example address 192.168.1.20 and a
# random token made here. Needs Go, herdr, vhs, ffmpeg (gifsicle optional).
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
. "$here/lib.sh"
work=${WORK:-$(mktemp -d -t sheepdog-plugin)}
out=$repo/assets/readme
herdr=$(command -v herdr)

# The plugin runs bin/sheepdog from its own directory (`plugin link`
# skips the build step that would download it).
(cd "$repo" && go build -o bin/sheepdog ./cmd/sheepdog)
# A short HOME: herdr's socket path must fit a unix socket address.
home=$(mktemp -d "${TMPDIR:-/tmp}/sdp.XXXX")
home=$(cd "$home" && pwd)
cleanup() {
	env -i HOME="$home" XDG_CONFIG_HOME="$home/.config" PATH=/usr/bin:/bin \
		"$herdr" server stop >/dev/null 2>&1 || true
	rm -rf "$home"
}
trap cleanup EXIT

# The plugin is linked by a path under the throwaway HOME, so no real
# path shows; herdr starts in ~/calc, which names its workspace.
ln -s "$repo" "$home/herdr-sheepdog"
mkdir -p "$home/calc" "$home/.config/herdr" "$home/.config/sheepdog" "$home/bin"
ln -s "$herdr" "$home/bin/herdr"
printf 'onboarding = false\n' >"$home/.config/herdr/config.toml"
printf '[web]\nbind = "192.168.1.20"\ntoken = "%s"\n' "$(openssl rand -hex 16)" >"$home/.config/sheepdog/config.toml"
for rc in .bashrc .bash_profile; do
	printf "PS1='\$ '\nexport BASH_SILENCE_DEPRECATION_WARNING=1\n" >"$home/$rc"
done

cp "$here/plugin.tape" "$work/"
(cd "$work" && env -i HOME="$HOME" PATH="$PATH" TERM=xterm-256color \
	SDHOME="$home" SDPATH="$home/bin:/usr/bin:/bin" vhs plugin.tape)

mkdir -p "$out"
ffmpeg -loglevel error -y -i "$work/plugin.mp4" -c:v libx264 -pix_fmt yuv420p -crf 24 -movflags +faststart -an "$out/plugin.mp4"
to_gif "$work/plugin.mp4" "$out/plugin.gif" 800 12
echo "work files in $work"
