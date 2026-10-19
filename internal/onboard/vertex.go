package onboard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
)

// Vertex AI key checks (the setup page's "Google Cloud (Vertex AI)"
// block). Vertex's model listing refuses an API key (brain.VertexModels),
// so the key and project are proved with countTokens on the orchestrator's
// default model at location global (free, the endpoint the vertex
// orchestrator provider calls), and, for the voice, the location with a
// setup-only Live handshake (no audio, closed right after setupComplete):
// REST cannot tell whether a location serves the Live model.

// VertexCreds are the [gemini.vertex] values a check uses.
type VertexCreds struct {
	APIKey, Project, Location string
}

// VertexAPIURL is the console page that enables the Vertex AI API.
const VertexAPIURL = "https://console.cloud.google.com/apis/library/aiplatform.googleapis.com"

// VertexKeyDocURL explains the key Vertex needs.
const VertexKeyDocURL = "https://github.com/kccarlos/herdr-sheepdog/blob/main/docs/models.md#vertex-ai"

// vertexBase is ep.VertexBase or the global Vertex host.
func (e Endpoints) vertexBase() string {
	if e.VertexBase != "" {
		return strings.TrimSuffix(e.VertexBase, "/")
	}
	return strings.TrimSuffix(brain.VertexBaseURL, "/")
}

// googleError is a Google API error body's fields the mapping reads.
type googleError struct {
	Status  int
	Code    string // error.status, e.g. PERMISSION_DENIED
	Message string
	Reasons []string // error.details[].reason, e.g. SERVICE_DISABLED
}

func (e *googleError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.Status, e.Message) }

func parseGoogleError(status int, data []byte, fallback string) *googleError {
	var body struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
			Details []struct {
				Reason string `json:"reason"`
			} `json:"details"`
		} `json:"error"`
	}
	ge := &googleError{Status: status, Message: apiMessage(data, fallback)}
	if json.Unmarshal(data, &body) == nil {
		ge.Code = body.Error.Status
		for _, d := range body.Error.Details {
			if d.Reason != "" {
				ge.Reasons = append(ge.Reasons, d.Reason)
			}
		}
	}
	return ge
}

func (e *googleError) has(reason string) bool {
	for _, r := range e.Reasons {
		if r == reason {
			return true
		}
	}
	return false
}

// vertexCountTokens is the key and project check: one countTokens call on
// brain.DefaultModel at global, the key in the x-goog-api-key header.
func vertexCountTokens(ctx context.Context, ep Endpoints, v VertexCreds) error {
	u := fmt.Sprintf("%s/v1/projects/%s/locations/%s/publishers/google/models/%s:countTokens",
		ep.vertexBase(), url.PathEscape(v.Project), brain.VertexLocation, brain.DefaultModel)
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", v.APIKey)
	resp, err := ep.client().Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return parseGoogleError(resp.StatusCode, data, resp.Status)
	}
	return nil
}

// CheckVertex validates Vertex AI credentials live: the key and project
// always, and with liveModel set (the voice runs on Vertex) that the
// location serves that Live model. The key never appears in the result.
func CheckVertex(ctx context.Context, ep Endpoints, v VertexCreds, liveModel string) Result {
	const name = "vertex"
	v.APIKey, v.Project, v.Location = strings.TrimSpace(v.APIKey), strings.TrimSpace(v.Project), strings.TrimSpace(v.Location)
	switch {
	case v.APIKey == "":
		return Result{Name: name, Status: Fail, Detail: "no Vertex AI API key given", Fix: "paste a Vertex AI API key bound to a service account (" + VertexKeyDocURL + ")"}
	case v.Project == "":
		return Result{Name: name, Status: Fail, Detail: "no Google Cloud project id given", Fix: "type the project id (the Google Cloud console shows it next to the project name)"}
	case liveModel != "" && v.Location == "":
		return Result{Name: name, Status: Fail, Detail: "no location given", Fix: "use us-central1"}
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	r := vertexResult(v, vertexCountTokens(ctx, ep, v))
	if r.Status == Fail || liveModel == "" {
		return r
	}
	live := ep.VertexLive
	if live == nil {
		return r
	}
	if err := live(ctx, v.APIKey, v.Project, v.Location, liveModel); err != nil {
		return vertexLiveResult(v, liveModel, err)
	}
	r.Detail = fmt.Sprintf("Vertex AI accepted the key for project %s, and %s serves %s", v.Project, v.Location, liveModel)
	return r
}

// vertexResult maps the countTokens answer to a plain Result.
func vertexResult(v VertexCreds, err error) Result {
	const name = "vertex"
	if err == nil {
		return Result{Name: name, Status: OK, Detail: "Vertex AI accepted the key for project " + v.Project}
	}
	enable := VertexAPIURL + "?project=" + url.QueryEscape(v.Project)
	ge, ok := err.(*googleError)
	if !ok {
		return Result{Name: name, Status: Fail, Detail: "Vertex AI unreachable: " + scrub(err.Error(), v.APIKey), Fix: "check the network, then try again"}
	}
	msg := strings.ToLower(ge.Message)
	shown := scrub(ge.Message, v.APIKey)
	switch {
	case strings.Contains(msg, "terms of use") || strings.Contains(msg, "terms of service") || strings.Contains(msg, "trust and safety") || strings.Contains(msg, "trust & safety"):
		return Result{Name: name, Status: Fail, Detail: "Google declined requests from project " + v.Project + " under its terms of use (HTTP " + fmt.Sprint(ge.Status) + "). This is a decision on the Google Cloud account, not a problem with the key or with sheepdog.", Fix: "Google Cloud support can review it; until then, choose another voice or orchestrator"}
	case ge.has("SERVICE_DISABLED") || strings.Contains(msg, "has not been used in project") || strings.Contains(msg, "it is disabled"):
		return Result{Name: name, Status: Fail, Detail: "the Vertex AI API is not enabled in project " + v.Project, Fix: "enable it at " + enable + ", wait a minute, then try again"}
	case ge.has("API_KEY_SERVICE_BLOCKED") || strings.Contains(msg, "are blocked"):
		return Result{Name: name, Status: Fail, Detail: "this key is not allowed to call Vertex AI", Fix: "restrict the key to aiplatform.googleapis.com (Vertex AI API), not to other APIs only"}
	case ge.has("API_KEY_INVALID") || strings.Contains(msg, "api key not valid") || strings.Contains(msg, "api keys are not supported") || ge.Status == http.StatusUnauthorized:
		return Result{Name: name, Status: Fail, Detail: "Vertex AI rejected the key", Fix: "check that you copied the whole key; it must be a Vertex AI API key bound to a service account (" + VertexKeyDocURL + ")"}
	case ge.has("CONSUMER_INVALID") || ge.Status == http.StatusNotFound || (ge.Status == http.StatusBadRequest && strings.Contains(msg, "project")):
		return Result{Name: name, Status: Fail, Detail: "project " + v.Project + " was not found, or this key cannot use it", Fix: "check the project id (not the project name or number), and that the Vertex AI API is enabled there: " + enable}
	case ge.Status == http.StatusForbidden:
		return Result{Name: name, Status: Fail, Detail: "this key may not use Vertex AI in project " + v.Project + " (" + shown + ")", Fix: "give the key's service account the Vertex AI User role in that project, and check that the Vertex AI API is enabled: " + enable}
	case ge.Status == http.StatusTooManyRequests:
		return Result{Name: name, Status: Warn, Detail: "the key and project work, but Vertex AI is rate limiting this project right now", Fix: "wait a minute; raise the quota in the Google Cloud console if it keeps happening"}
	}
	return Result{Name: name, Status: Fail, Detail: fmt.Sprintf("Vertex AI answered HTTP %d: %s", ge.Status, shown), Fix: "check the key and the project id, then try again"}
}

// vertexLiveResult maps a failed Live handshake (the key and project
// already passed): the usual case is a location without the Live model.
func vertexLiveResult(v VertexCreds, model string, err error) Result {
	const name = "vertex"
	msg := strings.ToLower(err.Error())
	switch {
	// The close reason is cut at 123 bytes, often before "found":
	// "… Publisher model `projects/…/gemini-3.8-live` was not".
	case strings.Contains(msg, "not found") || strings.Contains(msg, "publisher model") || strings.Contains(msg, "no such host"):
		return Result{Name: name, Status: Fail, Detail: fmt.Sprintf("location %s does not serve %s", v.Location, model), Fix: "use us-central1 (or the us multi-region); global does not serve the Live model"}
	case strings.Contains(msg, "terms of use"):
		return vertexResult(v, &googleError{Status: http.StatusForbidden, Message: err.Error()})
	case strings.Contains(msg, "deadline exceeded") || strings.Contains(msg, "context canceled"):
		return Result{Name: name, Status: Fail, Detail: fmt.Sprintf("the Live model at %s did not answer in time", v.Location), Fix: "try again; if it keeps happening, use us-central1"}
	}
	return Result{Name: name, Status: Fail, Detail: fmt.Sprintf("the key works, but the Live model at %s refused the connection: %s", v.Location, scrub(err.Error(), v.APIKey)), Fix: "use us-central1, and check that the key's service account has the Vertex AI User role"}
}

// scrub removes key from s (an API message never carries it today; this
// keeps it that way).
func scrub(s, key string) string {
	if key == "" {
		return s
	}
	return strings.ReplaceAll(s, key, "[key]")
}
