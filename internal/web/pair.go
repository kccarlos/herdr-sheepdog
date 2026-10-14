package web

import (
	"net"
	"net/http"
	"strconv"

	"github.com/kccarlos/herdr-sheepdog/internal/pair"
)

// PairResponse is GET /api/pair: the addresses the pairing code holds
// (never the token itself), or why there are none.
type PairResponse struct {
	Addresses []pair.Address `json:"addresses"`
	HasToken  bool           `json:"has_token"`
	Error     string         `json:"error,omitempty"`
}

// pairSysAddrs lists the machine's addresses (tests replace it).
var pairSysAddrs = pair.SystemAddrs

// pairing is the pairing link for the address the server listens on.
func (s *Server) pairing() ([]pair.Address, string, error) { return s.pairingFor(s.Addr()) }

// pairingFor is the pairing link for a server listening on addr.
func (s *Server) pairingFor(addr string) ([]pair.Address, string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, "", err
	}
	p, _ := strconv.Atoi(port)
	sys, err := pairSysAddrs()
	if err != nil {
		return nil, "", err
	}
	addrs, err := pair.Addresses(host, p, sys)
	if err != nil {
		return nil, "", err
	}
	return addrs, pair.URL(addrs, s.deps.Token), nil
}

// handlePair answers the addresses for /debug's pairing panel.
func (s *Server) handlePair(w http.ResponseWriter, _ *http.Request) {
	addrs, _, err := s.pairing()
	resp := PairResponse{Addresses: addrs, HasToken: s.deps.Token != ""}
	if resp.Addresses == nil {
		resp.Addresses = []pair.Address{}
	}
	if err != nil {
		resp.Error = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

// handlePairQR serves the pairing code as SVG (it holds the token: the
// route is behind the token like the rest of /api, and nothing logs the
// body).
func (s *Server) handlePairQR(w http.ResponseWriter, _ *http.Request) {
	_, link, err := s.pairing()
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	svg, err := pair.SVG(link)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(svg))
}
