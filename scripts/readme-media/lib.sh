# Shared helpers for the README media scripts (sourced, not run).

# to_gif IN OUT WIDTH FPS [EXTRA_FILTER]: a palette GIF, scaled to WIDTH,
# then squeezed with gifsicle when it is installed.
to_gif() {
	local in=$1 out=$2 width=$3 fps=$4 extra=${5:-}
	local pre="fps=$fps,scale=$width:-1:flags=lanczos"
	[ -n "$extra" ] && pre="$extra,$pre"
	ffmpeg -loglevel error -y -i "$in" -filter_complex \
		"$pre,split[a][b];[a]palettegen=stats_mode=diff:max_colors=128[p];[b][p]paletteuse=dither=bayer:bayer_scale=4:diff_mode=rectangle" \
		"$out"
	if command -v gifsicle >/dev/null; then
		gifsicle -O3 --batch "$out"
	fi
	ls -l "$out" | awk '{printf "%s: %.0f KB\n", $NF, $5/1024}'
}
