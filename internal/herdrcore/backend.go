package herdrcore

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

var _ herdr.Backend = (*Backend)(nil)

// DefaultFallbackPoll is how often an agent that does not report status is
// read to find out whether it shows a question.
const DefaultFallbackPoll = 2 * time.Second

// Backend is the herdr-core implementation of herdr.Backend. Create it with
// New; set the exported fields before the first call.
type Backend struct {
	// FallbackPoll is the period of the screen-fallback poll in a pane
	// subscription. Zero uses DefaultFallbackPoll.
	FallbackPoll time.Duration
	// Backoff paces subscription reconnects. Zero uses herdr.DefaultBackoff.
	Backoff herdr.Backoff
	// LookPath finds an executable on PATH (exec.LookPath by default); it is
	// a field so tests need no real agents installed.
	LookPath func(file string) (string, error)

	client *hc.Client
	log    herdr.Logger

	resync chan struct{}

	mu   sync.Mutex
	pong herdr.Pong // the last hello, zero until the first
	// launches are StartAgent's launches not yet seen as agents, by
	// terminal (agents.go).
	launches map[string]launch
}

// New returns a backend over client. A nil log discards events.
func New(client *hc.Client, log herdr.Logger) *Backend {
	if log == nil {
		log = herdr.NopLogger{}
	}
	return &Backend{
		LookPath: exec.LookPath,
		client:   client,
		log:      log,
		resync:   make(chan struct{}, 1),
		launches: map[string]launch{},
	}
}

// Ping performs hc's hello handshake. The version is cached for Snapshot.
func (b *Backend) Ping(ctx context.Context) (herdr.Pong, error) {
	hello, err := b.client.Hello(ctx)
	if err != nil {
		return herdr.Pong{}, err
	}
	pong := herdr.Pong{Version: hello.Version, Protocol: int(hello.Generation)}
	b.mu.Lock()
	b.pong = pong
	b.mu.Unlock()
	return pong, nil
}

// Resync returns the channel signalled after a subscription reconnect or a
// missed notification: the snapshot should be read again.
func (b *Backend) Resync() <-chan struct{} { return b.resync }

func (b *Backend) signalResync() {
	select {
	case b.resync <- struct{}{}:
	default:
	}
}

// logEvent records an event from source herdr, tagged as the hc backend.
func (b *Backend) logEvent(typ, paneID string, fields map[string]any) {
	fields["backend"] = "hc"
	b.log.Log("herdr", typ, paneID, fields)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// translate makes an hc error also match herdr's sentinel errors, keeping
// the hc error (and its hint) in the chain.
func translate(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, hc.ErrBlocked):
		return fmt.Errorf("%w: %w", herdr.ErrAgentBlocked, err)
	case errors.Is(err, hc.ErrNotFound), errors.Is(err, hc.ErrFinished):
		// A finished terminal is no longer a pane.
		return fmt.Errorf("%w: %w", herdr.ErrPaneNotFound, err)
	}
	return err
}
