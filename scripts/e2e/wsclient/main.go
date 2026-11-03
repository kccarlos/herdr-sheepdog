// Command wsclient holds the voice on a daemon as a macOS /ws client with no
// audio, so /api/state shows a real voice.client (docs/dev/e2e.md,
// scenario voice-client). It sends client-ready, drains every frame and,
// with -say, sends one text message once attached (a daemon running
// --voice=fake then "speaks"). It runs until killed or until the daemon
// closes the socket, and gives up after -max.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/coder/websocket"
)

func main() {
	url := flag.String("url", "ws://127.0.0.1:7777/ws", "daemon /ws address")
	name := flag.String("name", "sheepdog-mac", "client name")
	platform := flag.String("platform", "macos", "client platform")
	say := flag.String("say", "", "send this text message once attached")
	max := flag.Duration("max", 30*time.Minute, "give up after")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *max)
	defer cancel()

	dctx, dcancel := context.WithTimeout(ctx, 10*time.Second)
	c, _, err := websocket.Dial(dctx, *url, nil)
	dcancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wsclient: dial:", err)
		os.Exit(1)
	}
	defer func() { _ = c.CloseNow() }()

	ready := fmt.Sprintf(`{"type":"client-ready","protocol":1,"client":{"name":%q,"version":"e2e","platform":%q}}`, *name, *platform)
	if err := c.Write(ctx, websocket.MessageText, []byte(ready)); err != nil {
		fmt.Fprintln(os.Stderr, "wsclient: client-ready:", err)
		os.Exit(1)
	}
	fmt.Println("wsclient: attached")
	if *say != "" {
		msg := fmt.Sprintf(`{"type":"text","text":%q}`, *say)
		if err := c.Write(ctx, websocket.MessageText, []byte(msg)); err != nil {
			fmt.Fprintln(os.Stderr, "wsclient: text:", err)
			os.Exit(1)
		}
	}
	for {
		if _, _, err := c.Read(ctx); err != nil {
			if ctx.Err() != nil {
				_ = c.Close(websocket.StatusNormalClosure, "bye")
				return
			}
			fmt.Println("wsclient: closed by the daemon:", err)
			return
		}
	}
}
