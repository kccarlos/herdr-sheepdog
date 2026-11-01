# herdr-app.gif, terminal half: the documented curl installer and the real
# `sheepdog setup`. Run through herdr-app.sh, which serves this tree's
# installer, builds the release tarball and creates all private state.

Output herdr-app.mp4
Set Shell "bash"
Set FontSize 12
Set Width 800
Set Height 500
Set Padding 14
Set Theme "Github"
Set TypingSpeed 24ms

Hide
Type "export HOME=$SDHOME PATH=$SDPATH SHEEPDOG_RELEASE_URL=$SDRELEASE HERDR_APP_HOME=$SDAPP HC_SOCKET=$SDHC_SOCKET HC_SESSION=sda XDG_STATE_HOME=$SDHC_STATE XDG_RUNTIME_DIR=$SDWORK SHELL=/bin/bash BASH_SILENCE_DEPRECATION_WARNING=1 PS1='$ ' && cd ~ && unset HISTFILE"
Enter
Type `sh() { command /bin/sh "$@" 2>&1 | awk -v home="$HOME" '{ gsub(home, "~"); print; fflush(); if ($0 ~ /^Switched herdr-app to the Sheepdog layout\./) exit }'; }; clear`
Enter
Sleep 300ms
Show

Type "curl -fsSL https://raw.githubusercontent.com/kccarlos/herdr-sheepdog/main/scripts/install.sh | sh"
Enter
Wait+Screen /Run sheepdog setup now\?/
Sleep 300ms
Enter
Wait+Screen /Sheepdog\.app/
Sleep 1s
Enter
Sleep 3s
Enter
Wait+Screen /Switched herdr-app to the Sheepdog layout\./
Sleep 1s
