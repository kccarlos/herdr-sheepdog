# The hero video's desk shot: herdr (throwaway HOME, see desk.sh) with
# four scripted demo agents. desk.sh sets SDHOME, SDPATH, SDHERO, MODE
# (and desk.sh fills in @HOLD@), and renames the output.

Output desk.mp4
Set Shell "bash"
Set FontSize 22
Set Width 1920
Set Height 1080
Set Padding 16
Set Theme "Github"

Hide
Type "export HOME=$SDHOME XDG_CONFIG_HOME=$SDHOME/.config PATH=$SDPATH SHELL=/bin/bash BASH_SILENCE_DEPRECATION_WARNING=1 PS1='$ ' && cd ~/shop && unset HISTFILE && clear"
Enter
Sleep 300ms
Type "herdr"
Enter
Sleep 2.5s
Type "clear; exec $SDHERO/desk-setup.sh $MODE"
Enter
Sleep 2.5s
Show
Sleep @HOLD@
