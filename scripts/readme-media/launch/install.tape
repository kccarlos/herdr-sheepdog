# install.gif, the terminal half: the plugin install and `sheepdog setup`.
# Run through scripts/readme-media/launch/install-gif.sh, which makes the
# throwaway HOME and sets SDHOME / SDPATH for the hidden setup below.

Output terminal.mp4
Set Shell "bash"
Set FontSize 13
Set Width 800
Set Height 560
Set Padding 14
Set Theme "Github"
Set TypingSpeed 40ms

Hide
Type "export HOME=$SDHOME PATH=$SDPATH BASH_SILENCE_DEPRECATION_WARNING=1 PS1='$ ' && cd ~ && unset HISTFILE"
Enter
Type `herdr() { if [ "$1 $2" = "plugin install" ]; then sh "$SDHOME/herdr-sheepdog/scripts/plugin-fetch.sh" 2>&1 | sed "s#$SDHOME#~#g"; else command herdr "$@"; fi; }; sheepdog() { command sheepdog "$@" 2>&1 | sed "s#$SDHOME#~#g"; }; clear`
Enter
Sleep 300ms
Show

Type "herdr plugin install kccarlos/herdr-sheepdog"
Sleep 300ms
Enter
Sleep 1.8s
Type "sheepdog setup"
Sleep 300ms
Enter
Sleep 2.2s
