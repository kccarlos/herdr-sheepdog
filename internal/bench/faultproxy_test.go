package bench

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFaultProxy(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "upstream "+r.URL.Path)
	}))
	defer up.Close()
	p, err := StartFaultProxy(up.URL+"/v1", Faults{BrainFailFirst: 2, BrainDelayMS: 50})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	codes := []int{}
	var body string
	for i := 0; i < 3; i++ {
		t0 := time.Now()
		resp, err := http.Post(p.URL+"/chat/completions", "application/json", nil)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		codes = append(codes, resp.StatusCode)
		body = string(b)
		if time.Since(t0) < 50*time.Millisecond {
			t.Fatal("no delay")
		}
	}
	if codes[0] != 503 || codes[1] != 503 || codes[2] != 200 || body != "upstream /v1/chat/completions" {
		t.Fatalf("codes %v body %q", codes, body)
	}
	if r, f := p.Stats(); r != 3 || f != 2 {
		t.Fatalf("stats %d %d", r, f)
	}
}
