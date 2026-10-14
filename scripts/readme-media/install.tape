# install.gif: `sheepdog init` and `sheepdog pair` in a throwaway home.
# Run through scripts/readme-media/install.sh, which builds sheepdog,
# makes the throwaway HOME (with a stand-in herdr socket) and sets
# SDHOME / SDBIN for the hidden setup below. No real key or token: the
# key is a placeholder (so --skip-validate), the web token is generated
# into the throwaway home and thrown away, and the address is an example.

Output install.mp4
Set Shell "bash"
Set FontSize 12
Set Width 800
Set Height 630
Set Padding 14
Set Theme "Github"
Set TypingSpeed 35ms

Hide
Type "export HOME=$SDHOME PATH=$SDBIN:/usr/bin:/bin SHEEPDOG_WEB_BIND=192.168.1.20 PS1='$ ' && cd ~ && unset HISTFILE"
Enter
# Show $HOME as ~ in init's output, as a shell prompt would (pair keeps
# the terminal, for its colour QR code).
Type `sheepdog() { if [ "$1" = init ]; then command sheepdog "$@" 2>&1 | sed "s#$HOME#~#g"; else command sheepdog "$@"; fi; }; clear`
Enter
Sleep 300ms
Show

Type "export GEMINI_API_KEY=your-ai-studio-key"
Enter
Sleep 300ms
Type "sheepdog init --non-interactive --skip-validate"
Enter
Sleep 2.2s
Type "sheepdog pair"
Enter
Sleep 3.5s
