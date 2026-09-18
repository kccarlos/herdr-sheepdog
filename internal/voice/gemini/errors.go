package gemini

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Errors surfaced through Start, Send* and EventError.
var (
	// ErrAllKeysExhausted wraps the last rate-limit error after every key
	// in the pool was rejected at connect.
	ErrAllKeysExhausted = errors.New("gemini: all API keys rate limited")
	// ErrNotStarted is returned by Send* before Start.
	ErrNotStarted = errors.New("gemini: session not started")
	// ErrClosed is returned after Close.
	ErrClosed = errors.New("gemini: session closed")
	// ErrNotConnected is returned by SendText while reconnecting.
	ErrNotConnected = errors.New("gemini: not connected")
)

// isRateLimited reports whether err looks like an HTTP 429 /
// RESOURCE_EXHAUSTED, whether it arrived as a WebSocket close frame, an
// error frame or a handshake failure message.
func isRateLimited(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, needle := range []string{"resource_exhausted", "429", "quota", "rate limit", "rate-limit", "too many requests"} {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

// isRotatable reports whether a connect failure should move to the next
// key: rate limits, and WebSocket handshake rejections, whose HTTP status
// the SDK discards (a 429 or 403 at the handshake both look like
// "bad handshake").
func isRotatable(err error) bool {
	return isRateLimited(err) || (err != nil && strings.Contains(err.Error(), "bad handshake"))
}

var retryDelayRE = regexp.MustCompile(`(?i)(?:retrydelay"?\s*:\s*"?|retry (?:in|after)\s+)(\d+(?:\.\d+)?)\s*s`)

// retryDelay extracts RetryInfo.retryDelay ("30s") or "retry in 12.5s"
// from an error message; ok is false when there is none.
func retryDelay(err error) (time.Duration, bool) {
	if err == nil {
		return 0, false
	}
	m := retryDelayRE.FindStringSubmatch(err.Error())
	if m == nil {
		return 0, false
	}
	secs, perr := strconv.ParseFloat(m[1], 64)
	if perr != nil || secs <= 0 {
		return 0, false
	}
	return time.Duration(secs * float64(time.Second)), true
}
