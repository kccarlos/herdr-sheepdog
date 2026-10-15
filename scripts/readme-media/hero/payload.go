//go:build ignore

// payload.go prints the APNs payload the daemon would send for the iOS
// demo world's approval (codex-payments asks to run npm install p-retry), built
// by the daemon's own internal/push code, plus the "Simulator Target
// Bundle" key `xcrun simctl push` needs.
//
//	go run scripts/readme-media/hero/payload.go > payload.apns
package main

import (
	"encoding/json"
	"os"

	"github.com/kccarlos/herdr-sheepdog/internal/push"
)

func main() {
	n := push.Notification{
		PendingID: "demo-ap-1",
		PaneID:    "demo-codex-payments",
		Agent:     "codex-payments",
		Kind:      push.KindApproval,
		// The demo world's spoken line (DemoWorld.askLine), as the
		// daemon puts the voice's sentence in the alert.
		Text: "codex-payments wants to run npm install p-retry. Approve?",
		Options: []push.Option{
			{Index: 1, Label: "Yes, proceed"},
			{Index: 2, Label: "No, and tell Codex what to do differently"},
		},
		NeedsYou: true,
	}
	p := n.Payload()
	p["Simulator Target Bundle"] = "com.github.kccarlos.herdr-sheepdog-ios"
	e := json.NewEncoder(os.Stdout)
	e.SetIndent("", "  ")
	if err := e.Encode(p); err != nil {
		panic(err)
	}
}
