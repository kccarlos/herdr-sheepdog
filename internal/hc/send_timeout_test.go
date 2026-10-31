package hc_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

// slowCore answers every request after delay, the way herdr-core answers
// terminal.send only once the terminal has been quiet for a while.
func slowCore(t *testing.T, delay time.Duration) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "c.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer func() { _ = conn.Close() }()
				line, err := bufio.NewReader(conn).ReadBytes('\n')
				if err != nil {
					return
				}
				var req struct {
					ID json.RawMessage `json:"id"`
				}
				_ = json.Unmarshal(line, &req)
				time.Sleep(delay)
				_, _ = conn.Write([]byte(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":{}}` + "\n"))
			}(conn)
		}
	}()
	return path
}

// terminal.send outlives the client's usual timeout; other calls do not.
func TestSendWaitsLongerThanOtherCalls(t *testing.T) {
	client := hc.NewClient(slowCore(t, 200*time.Millisecond))
	client.Timeout = 50 * time.Millisecond
	ctx := context.Background()
	if _, err := client.Read(ctx, "t1", 0); err == nil {
		t.Fatal("read answered after 200 ms should time out at 50 ms")
	}
	if _, err := client.Send(ctx, "t1", "hello", false); err != nil {
		t.Fatalf("send should wait for herdr-core's quiet window: %v", err)
	}
}
