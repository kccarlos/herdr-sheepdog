package web

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"
)

func TestEventsKindsFilter(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.log.Log("herdr", "herdr.state", "w1:p1", map[string]any{"state": "working"})
	h.log.Log("supervisor", "pending.created", "w1:p1", map[string]any{"id": "p1"})
	h.log.Log("voice", "voice.push", "", map[string]any{"text": "hello"})
	h.log.Log("supervisor", "supervisor.note", "", map[string]any{"text": "note"})

	tests := []struct {
		name      string
		query     string
		want      []string
		wantError string
	}{
		{
			name:  "no kinds leaves the stream unchanged",
			query: "/events?n=4",
			want:  []string{"herdr.state", "pending.created", "voice.push", "supervisor.note"},
		},
		{
			name:  "one kind",
			query: "/events?n=4&kinds=voice",
			want:  []string{"voice.push"},
		},
		{
			name:  "several kinds",
			query: "/events?n=4&kinds=pending,%20voice",
			want:  []string{"pending.created", "voice.push"},
		},
		{
			name:      "unknown kind",
			query:     "/events?kinds=voice,nope",
			wantError: `unknown event kind "nope"`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.wantError != "" {
				resp, err := http.Get(h.http.URL + tc.query)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = resp.Body.Close() }()
				if resp.StatusCode != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400", resp.StatusCode)
				}
				var body map[string]string
				if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body["error"] != tc.wantError {
					t.Fatalf("error = %q, want %q", body["error"], tc.wantError)
				}
				return
			}

			c := openSSE(t, h.http.URL+tc.query)
			var got []string
			for range tc.want {
				ev, ok := c.next(t, 2*time.Second)
				if !ok {
					t.Fatalf("events = %v, want %v", got, tc.want)
				}
				got = append(got, ev.Type)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("events = %v, want %v", got, tc.want)
			}
			if ev, ok := c.next(t, 100*time.Millisecond); ok {
				t.Fatalf("unexpected event %s after %v", ev.Type, got)
			}
		})
	}
}
