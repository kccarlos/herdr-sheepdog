// Command relaystub stands in for sheepdog's push relay in the private e2e
// environment (docs/dev/e2e.md, up.sh --push-stub): it answers the relay's
// API on a loopback port and appends one JSON line per request to a file, so
// a scenario can tell a push that was sent from one that was not. It sends
// nothing anywhere: no Apple, no network beyond 127.0.0.1.
//
//	relaystub -addr 127.0.0.1:18101 -log /tmp/sde-xxxx/relay.jsonl
//
// Lines: {"path":"/v1/send","kind":"approval","priority":10,"collapse_id":"…"}.
// The payload is sealed with the phone's key, so nothing in it is read here.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "listen address (loopback only)")
	logPath := flag.String("log", "", "file to append one JSON line per request to")
	flag.Parse()
	if *logPath == "" {
		fmt.Fprintln(os.Stderr, "relaystub: -log is required")
		os.Exit(2)
	}
	var mu sync.Mutex
	record := func(line map[string]any) {
		mu.Lock()
		defer mu.Unlock()
		f, err := os.OpenFile(*logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		defer func() { _ = f.Close() }()
		b, _ := json.Marshal(line)
		_, _ = f.Write(append(b, '\n'))
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"ok":true,"version":"e2e-stub"}`)
	})
	mux.HandleFunc("POST /v1/register", func(w http.ResponseWriter, r *http.Request) {
		record(map[string]any{"path": r.URL.Path})
		_, _ = io.WriteString(w, `{"relay_token":"e2e-stub-relay-token-0001"}`)
	})
	mux.HandleFunc("POST /v1/send", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Payload struct {
				Kind       string `json:"kind"`
				CollapseID string `json:"collapse_id"`
			} `json:"payload"`
			Priority int `json:"priority"`
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)
		record(map[string]any{"path": r.URL.Path, "kind": body.Payload.Kind, "priority": body.Priority, "collapse_id": body.Payload.CollapseID})
		_, _ = io.WriteString(w, `{}`)
	})
	srv := &http.Server{Addr: *addr, Handler: mux}
	fmt.Println("relaystub listening on", *addr)
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, "relaystub:", err)
		os.Exit(1)
	}
}
