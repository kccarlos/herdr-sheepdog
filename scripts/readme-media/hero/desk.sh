#!/usr/bin/env bash
# The hero video's desk shots: vhs films herdr with four scripted demo
# agents (demo-agent.sh) in a throwaway HOME and XDG_CONFIG_HOME under a
# clean environment (env -i), so herdr's config, sessions and socket are
# new and thrown away; the real herdr is never reached. Two takes: the
# agents at work, and the same panes at the end of the story.
#
#   WORK=/tmp/hero scripts/readme-media/hero/desk.sh   # $WORK/desk/{working,done}.mp4
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
work=${WORK:?set WORK}/desk
mkdir -p "$work"
herdr=$(command -v herdr)

take() {
	local mode=$1 hold=$2
	local home
	home=$(mktemp -d "${TMPDIR:-/tmp}/sdh.XXXX")
	home=$(cd "$home" && pwd)
	mkdir -p "$home/shop" "$home/.config/herdr" "$home/bin"
	ln -s "$herdr" "$home/bin/herdr"
	printf 'onboarding = false\n' >"$home/.config/herdr/config.toml"
	for rc in .bashrc .bash_profile; do
		printf "PS1='\$ '\nexport BASH_SILENCE_DEPRECATION_WARNING=1\n" >"$home/$rc"
	done
	sed "s/@HOLD@/$hold/" "$here/desk.tape" >"$work/desk.tape"
	(cd "$work" && env -i HOME="$HOME" PATH="$PATH" TERM=xterm-256color \
		SDHOME="$home" SDPATH="$home/bin:/usr/bin:/bin" SDHERO="$here" MODE="$mode" vhs desk.tape)
	env -i HOME="$home" XDG_CONFIG_HOME="$home/.config" PATH=/usr/bin:/bin "$herdr" server stop >/dev/null 2>&1 || true
	rm -rf "$home"
	mv "$work/desk.mp4" "$work/$mode.mp4"
}
take working 12s
take done 6s
echo "desk takes in $work"
