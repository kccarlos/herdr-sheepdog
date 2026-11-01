# herdr-tui.gif: the real GitHub plugin install and the Pair your phone pane.
# Run through scripts/readme-media/herdr-tui.sh, which creates a clean HOME,
# herdr-app home, herdr-core session and free-port sheepdog config.

Output herdr-tui.mp4
Set Shell "bash"
Set FontSize 12
Set Width 800
Set Height 760
Set Padding 10
Set Theme "Github"
Set TypingSpeed 30ms

Hide
Type "export HOME=$SDHOME XDG_CONFIG_HOME=$SDHOME/.config HERDR_APP_HOME=$SDAPP HC_SOCKET=$SDHC_SOCKET HC_SESSION=sdtui XDG_STATE_HOME=$SDHC_STATE XDG_RUNTIME_DIR=$SDHOME PATH=$SDPATH SHELL=/bin/bash BASH_SILENCE_DEPRECATION_WARNING=1 PS1='$ ' && cd ~/calc && unset HISTFILE"
Enter
# Keep the throwaway HOME out of the installer's config line. `script`
# gives herdr a PTY so its preview and prompt still stream live.
Type `herdr() { /usr/bin/script -q /dev/null "$SDHERDR" "$@" | sed "s#$HOME#~#g"; }; clear`
Enter
Sleep 300ms
Show

Type "herdr plugin install kccarlos/herdr-sheepdog"
Enter
Sleep 24s
Type "y"
Enter
Wait+Screen /Installed kccarlos\.sheepdog/
Sleep 500ms

Hide
Type "sheepdog start --config ~/.config/sheepdog/config.toml >/dev/null && unset -f herdr; clear"
Enter
Sleep 200ms
Show

Type "herdr"
Enter
Wait+Screen /spaces/
Sleep 1s
Type "herdr plugin pane open --plugin kccarlos.sheepdog --entrypoint pair >/dev/null"
Enter
Wait+Screen /Scan in the Sheepdog app/
Sleep 12s
