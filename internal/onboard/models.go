package onboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
)

// Limits of a model-list call: a hostile or huge endpoint cannot fill
// memory or the page.
const (
	maxModelsBody  = 4 << 20
	maxModelsCount = 2000
	modelsTimeout  = 10 * time.Second
)

// ModelEntry is one model an endpoint lists.
type ModelEntry struct {
	ID string `json:"id"`
	// Tools is true when the endpoint says the model supports tool calls
	// (OpenRouter's supported_parameters has "tools"), false when it
	// lists parameters without "tools", nil when it says nothing.
	Tools *bool `json:"tools"`
}

// ModelsErrorKind says why a model list could not be had.
type ModelsErrorKind int

const (
	// ModelsUnreachable: no HTTP answer (DNS, refused, timeout) or a bad URL.
	ModelsUnreachable ModelsErrorKind = iota + 1
	// ModelsAuth: HTTP 401 or 403.
	ModelsAuth
	// ModelsNoList: 404, another status, a body that is not a model list,
	// or an empty list.
	ModelsNoList
)

// ModelsError is a failed ListModels call. Reason never holds the key.
type ModelsError struct {
	Kind   ModelsErrorKind
	Reason string
}

func (e *ModelsError) Error() string { return e.Reason }

// ListModels does GET {base}/models (Bearer key when given) and parses the
// ids. It accepts the OpenAI shape {data:[{id}]} that OpenAI, Groq, Ollama
// (/v1/models) and OpenRouter share; for OpenRouter's supported_parameters
// it also reports which models take tools. The key travels in a header,
// never in the URL; Go drops it on a cross-host redirect. client nil: a
// 10 s client. At most 2000 models and 4 MB of body are read.
func ListModels(ctx context.Context, client *http.Client, base, key string) ([]ModelEntry, error) {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, &ModelsError{Kind: ModelsUnreachable, Reason: "the Base URL must start with http:// or https://"}
	}
	if client == nil {
		client = &http.Client{Timeout: modelsTimeout}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(u.String(), "/")+"/models", nil)
	if err != nil {
		return nil, &ModelsError{Kind: ModelsUnreachable, Reason: "the Base URL is not valid"}
	}
	req.Header.Set("Accept", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, &ModelsError{Kind: ModelsUnreachable, Reason: reasonOf(err)}
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxModelsBody))
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, &ModelsError{Kind: ModelsAuth, Reason: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return nil, &ModelsError{Kind: ModelsNoList, Reason: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	case err != nil:
		return nil, &ModelsError{Kind: ModelsUnreachable, Reason: reasonOf(err)}
	}
	return parseModels(data)
}

// parseModels reads the {data:[...]} list.
func parseModels(data []byte) ([]ModelEntry, error) {
	var body struct {
		Data []struct {
			ID                  string    `json:"id"`
			SupportedParameters *[]string `json:"supported_parameters"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, &ModelsError{Kind: ModelsNoList, Reason: "the answer is not a model list"}
	}
	var out []ModelEntry
	seen := map[string]bool{}
	for _, d := range body.Data {
		id := strings.TrimSpace(d.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		e := ModelEntry{ID: id}
		if d.SupportedParameters != nil {
			has := false
			for _, p := range *d.SupportedParameters {
				if p == "tools" {
					has = true
				}
			}
			e.Tools = &has
		}
		out = append(out, e)
		if len(out) >= maxModelsCount {
			break
		}
	}
	if len(out) == 0 {
		return nil, &ModelsError{Kind: ModelsNoList, Reason: "the list is empty"}
	}
	return out, nil
}

// reasonOf is a short plain reason for a transport error, without the URL
// (which could carry credentials typed into it) or any header.
func reasonOf(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		if ue.Timeout() {
			return "no answer within 10 seconds"
		}
		err = ue.Err
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "no such host " + dns.Name
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "no answer in time"
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Err != nil {
		return op.Err.Error()
	}
	return err.Error()
}

// ProbeTools runs the doctor's tool-calling probe once on c.Model (a few
// tokens, which a paid endpoint may bill). It is for a user's click only.
// The Result is OK, Warn (the model answered without a tool call) or Fail.
func ProbeTools(ctx context.Context, ep Endpoints, c Compat) Result {
	name := c.Name + " tools"
	if c.Model == "" {
		return Result{Name: name, Status: Fail, Detail: "no model name given", Fix: "type the model name first"}
	}
	if c.APIKey == "" && !c.NoKey {
		return Result{Name: name, Status: Fail, Detail: "this endpoint needs an API key", Fix: "paste the key above, then press Test tool calling again"}
	}
	if ep.HTTP == nil {
		ep.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	m, err := brain.NewOpenAICompatModel(brain.OpenAICompatConfig{Provider: c.Name, BaseURL: c.BaseURL, APIKey: c.APIKey, NoKey: c.NoKey, Thinking: c.Thinking, HTTPClient: ep.client()})
	if err != nil {
		return Result{Name: name, Status: Fail, Detail: err.Error(), Fix: "check the Base URL"}
	}
	r := probeToolCall(ctx, m, c, consoleOf(c.Name, c.BaseURL))
	if strings.Contains(r.Fix, "doctor again") {
		r.Fix = "check the model name, the key and the Base URL, then press Test tool calling again"
	}
	return r
}

// KeyOptionalURL reports whether a server at base needs no key (loopback).
func KeyOptionalURL(base string) bool {
	return config.OpenAICompat{BaseURL: base}.KeyOptional()
}
