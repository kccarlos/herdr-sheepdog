package gemini

import (
	"context"
	"fmt"
	"sync"

	"google.golang.org/genai"
)

// Conn is the subset of *genai.Session the Session uses. It exists so the
// reconnect, push and tool logic can be unit-tested against a fake.
type Conn interface {
	SendRealtimeInput(genai.LiveRealtimeInput) error
	SendClientContent(genai.LiveClientContentInput) error
	SendToolResponse(genai.LiveToolResponseInput) error
	Receive() (*genai.LiveServerMessage, error)
	Close() error
}

// Dialer opens one Live connection with the given key. The SDK
// implementation is the default; tests inject a fake.
type Dialer interface {
	Dial(ctx context.Context, apiKey, model string, cfg *genai.LiveConnectConfig) (Conn, error)
}

// sdkDialer dials through google.golang.org/genai, one client per key.
type sdkDialer struct {
	mu      sync.Mutex
	clients map[string]*genai.Client
}

func newSDKDialer() *sdkDialer { return &sdkDialer{clients: map[string]*genai.Client{}} }

func (d *sdkDialer) client(ctx context.Context, apiKey string) (*genai.Client, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if c, ok := d.clients[apiKey]; ok {
		return c, nil
	}
	c, err := genai.NewClient(ctx, &genai.ClientConfig{APIKey: apiKey, Backend: genai.BackendGeminiAPI})
	if err != nil {
		return nil, fmt.Errorf("gemini: client: %w", err)
	}
	d.clients[apiKey] = c
	return c, nil
}

// Dial implements Dialer. The SDK's Connect ignores ctx while dialing, so
// the dial runs in a goroutine and a cancelled ctx abandons (and closes)
// the connection when it eventually completes.
func (d *sdkDialer) Dial(ctx context.Context, apiKey, model string, cfg *genai.LiveConnectConfig) (Conn, error) {
	client, err := d.client(ctx, apiKey)
	if err != nil {
		return nil, err
	}
	type result struct {
		s   *genai.Session
		err error
	}
	ch := make(chan result, 1)
	go func() {
		s, err := client.Live.Connect(ctx, model, cfg)
		ch <- result{s, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		return r.s, nil
	case <-ctx.Done():
		go func() {
			if r := <-ch; r.s != nil {
				_ = r.s.Close()
			}
		}()
		return nil, ctx.Err()
	}
}
