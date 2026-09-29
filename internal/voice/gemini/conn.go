package gemini

import (
	"context"
	"fmt"
	"net/http"
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
// With vertex set it dials Vertex AI instead of Google AI Studio.
type sdkDialer struct {
	vertex *Vertex
	// baseURL overrides the backend's base URL (tests only).
	baseURL string

	mu      sync.Mutex
	clients map[string]*genai.Client
}

func newSDKDialer(vertex *Vertex) *sdkDialer {
	return &sdkDialer{vertex: vertex, clients: map[string]*genai.Client{}}
}

// clientConfig is the genai client configuration for one key.
//
// On Vertex (spike 2026-09-29, docs/research/vertex-live-spike-2026-09-29.md)
// the Live socket is wss://{location}-aiplatform.googleapis.com/ws/
// google.cloud.aiplatform.v1beta1.LlmBidiService/BidiGenerateContent and
// accepts the API key as an x-goog-api-key header; the setup names the
// model as projects/{p}/locations/{l}/publishers/google/models/{m}, which
// the SDK builds from Project and Location. Two SDK gaps are filled here:
// with an API key it defaults to the global host, which does not serve
// the Live models, and it does not put the key on the Vertex socket.
func (d *sdkDialer) clientConfig(apiKey string) *genai.ClientConfig {
	if d.vertex == nil {
		return &genai.ClientConfig{APIKey: apiKey, Backend: genai.BackendGeminiAPI, HTTPOptions: genai.HTTPOptions{BaseURL: d.baseURL}}
	}
	base := d.baseURL
	if base == "" {
		base = vertexBaseURL(d.vertex.Location)
	}
	return &genai.ClientConfig{
		Backend:  genai.BackendVertexAI,
		APIKey:   apiKey,
		Project:  d.vertex.Project,
		Location: d.vertex.Location,
		HTTPOptions: genai.HTTPOptions{
			BaseURL: base,
			Headers: http.Header{"X-Goog-Api-Key": []string{apiKey}},
		},
	}
}

// vertexBaseURL is the Vertex AI endpoint for location: the global host,
// a multi-region (us, eu) host, or the regional one.
func vertexBaseURL(location string) string {
	switch location {
	case "global":
		return "https://aiplatform.googleapis.com/"
	case "us", "eu":
		return fmt.Sprintf("https://aiplatform.%s.rep.googleapis.com/", location)
	default:
		return fmt.Sprintf("https://%s-aiplatform.googleapis.com/", location)
	}
}

func (d *sdkDialer) client(ctx context.Context, apiKey string) (*genai.Client, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if c, ok := d.clients[apiKey]; ok {
		return c, nil
	}
	c, err := genai.NewClient(ctx, d.clientConfig(apiKey))
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
