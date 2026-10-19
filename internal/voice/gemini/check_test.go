package gemini

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"
)

type checkDialer struct {
	conn *fakeConn
	err  error
}

func (d checkDialer) Dial(context.Context, string, string, *genai.LiveConnectConfig) (Conn, error) {
	if d.err != nil {
		return nil, d.err
	}
	return d.conn, nil
}

func TestCheckLive(t *testing.T) {
	ok := newFakeConn("ok")
	ok.incoming <- &genai.LiveServerMessage{SetupComplete: &genai.LiveServerSetupComplete{}}
	if err := checkLive(context.Background(), checkDialer{conn: ok}, "k", "m"); err != nil {
		t.Errorf("setupComplete: %v", err)
	}
	select {
	case <-ok.closed:
	default:
		t.Error("the session was left open")
	}

	if err := checkLive(context.Background(), checkDialer{err: errors.New("dial refused")}, "k", "m"); err == nil || !strings.Contains(err.Error(), "dial refused") {
		t.Errorf("dial error: %v", err)
	}

	closed := newFakeConn("closed")
	closed.readErr = errors.New("websocket: close 1008 (policy violation): Publisher model was not found")
	close(closed.incoming)
	if err := checkLive(context.Background(), checkDialer{conn: closed}, "k", "m"); err == nil || !strings.Contains(err.Error(), "was not found") {
		t.Errorf("close 1008: %v", err)
	}

	other := newFakeConn("other")
	other.incoming <- &genai.LiveServerMessage{}
	if err := checkLive(context.Background(), checkDialer{conn: other}, "k", "m"); err == nil {
		t.Error("a first message without setupComplete passed")
	}

	silent := newFakeConn("silent")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := checkLive(ctx, checkDialer{conn: silent}, "k", "m"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("silent server: %v", err)
	}
}
