# plugin.gif: herdr with sheepdog linked as a plugin, opening the "Pair
# your phone" popup. Run through scripts/readme-media/plugin.sh, which
# builds sheepdog into the plugin's bin/, makes a throwaway HOME (its own
# herdr config, plugin registry and server socket, never the real ones)
# with a dummy sheepdog config (example address 192.168.1.20, a throwaway
# token) and sets SDHOME / SDPATH for the hidden setup below.

Output plugin.mp4
Set Shell "bash"
Set FontSize 12
Set Width 800
Set Height 760
Set Padding 10
Set Theme "Github"
Set TypingSpeed 35ms

Hide
Type "export HOME=$SDHOME XDG_CONFIG_HOME=$SDHOME/.config PATH=$SDPATH SHELL=/bin/bash BASH_SILENCE_DEPRECATION_WARNING=1 PS1='$ ' && cd ~/calc && unset HISTFILE && clear"
Enter
Sleep 300ms
Show

# link prints the manifest as JSON (and herdr plugin list the real
# paths), so the visible step stays quiet.
Type "herdr plugin link ~/herdr-sheepdog >/dev/null"
Enter
Sleep 1s
Type "herdr"
Enter
Sleep 2.5s
Type "herdr plugin pane open --plugin kccarlos.sheepdog --entrypoint pair >/dev/null"
Enter
Sleep 5s
