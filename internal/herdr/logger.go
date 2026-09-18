package herdr

import (
	"io"

	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

// Logger is the structured-event sink (plan §3.3), shared with the other
// components through internal/logging.
type Logger = logging.Logger

// NopLogger discards everything.
type NopLogger = logging.NopLogger

// JSONLLogger writes one JSON object per event.
type JSONLLogger = logging.JSONLLogger

// NewJSONLLogger returns a logger writing to w.
func NewJSONLLogger(w io.Writer) *JSONLLogger { return logging.NewJSONLLogger(w) }
