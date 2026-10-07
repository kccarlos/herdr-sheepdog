package bench

import (
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"
)

// FaultProxy sits between a scratch daemon and the orchestrator's DeepSeek API
// (deepseek.base_url): it fails the first requests with a server error
// (the orchestrator's failover to its next provider) and can delay every
// request (a slow provider). Everything else is forwarded unchanged.
type FaultProxy struct {
	URL string

	srv    *http.Server
	ln     net.Listener
	faults Faults

	mu       sync.Mutex
	requests int
	failed   int
}

// StartFaultProxy forwards to target with faults.
func StartFaultProxy(target string, f Faults) (*FaultProxy, error) {
	u, err := url.Parse(target)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &FaultProxy{URL: "http://" + ln.Addr().String(), ln: ln, faults: f} // the target path is prepended by the proxy
	rp := httputil.NewSingleHostReverseProxy(u)
	base := rp.Director
	rp.Director = func(r *http.Request) {
		base(r)
		r.Host = u.Host
	}
	rp.FlushInterval = -1
	p.srv = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.requests++
		fail := p.failed < f.BrainFailFirst
		if fail {
			p.failed++
		}
		p.mu.Unlock()
		if f.BrainDelayMS > 0 {
			select {
			case <-time.After(time.Duration(f.BrainDelayMS) * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
		if fail {
			status := f.BrainFailStatus
			if status == 0 {
				status = http.StatusServiceUnavailable
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"message":"bench fault proxy: injected failure","type":"server_error"}}`))
			return
		}
		rp.ServeHTTP(w, r)
	}), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = p.srv.Serve(ln) }()
	return p, nil
}

// Stats are the requests seen and the failures injected.
func (p *FaultProxy) Stats() (requests, failed int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requests, p.failed
}

// Close stops the proxy.
func (p *FaultProxy) Close() {
	if p != nil {
		_ = p.srv.Close()
	}
}
