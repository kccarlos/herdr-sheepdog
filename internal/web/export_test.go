package web

import (
	"net/http"
)

// Handler returns the routed handler (for httptest).
func (s *Server) Handler() http.Handler { return s.handler() }
