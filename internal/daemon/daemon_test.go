package daemon

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"
)

func TestAcquireIsSingleInstance(t *testing.T) {
	dir := t.TempDir()
	l, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	info := Info{PID: os.Getpid(), URL: "http://127.0.0.1:1", Version: "v1", StartedAt: time.Now()}
	if err := l.Write(info); err != nil {
		t.Fatal(err)
	}
	if got, ok := Running(dir); !ok || got.PID != info.PID || got.URL != info.URL {
		t.Errorf("Running = %+v %v", got, ok)
	}
	_, err = Acquire(dir)
	var running *ErrRunning
	if !errors.As(err, &running) || running.Info.PID != info.PID {
		t.Fatalf("second Acquire: %v", err)
	}
	want := "sheepdog is already running (pid " + strconv.Itoa(info.PID) + ", http://127.0.0.1:1)"
	if err.Error() != want {
		t.Errorf("message = %q, want %q", err, want)
	}
	l.Release()
	if _, ok := Running(dir); ok {
		t.Error("still running after Release")
	}
	l2, err := Acquire(dir)
	if err != nil {
		t.Fatalf("Acquire after Release: %v", err)
	}
	l2.Release()
}

func TestProbePort(t *testing.T) {
	sheep := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"name":"sheepdog","version":"v9"}`))
	}))
	defer sheep.Close()
	other := httptest.NewServer(http.NotFoundHandler())
	defer other.Close()
	port := func(s *httptest.Server) int { return s.Listener.Addr().(*net.TCPAddr).Port }
	if st, h := ProbePort(context.Background(), "127.0.0.1", port(sheep)); st != PortSheepdog || h.Version != "v9" {
		t.Errorf("sheepdog: %v %+v", st, h)
	}
	if st, _ := ProbePort(context.Background(), "0.0.0.0", port(other)); st != PortOther {
		t.Errorf("other: %v", st)
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	free := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	if st, _ := ProbePort(context.Background(), "", free); st != PortFree {
		t.Errorf("free: %v", st)
	}
}
