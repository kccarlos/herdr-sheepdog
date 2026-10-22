package web

import (
	"net"
	"sync"
)

// mergedListener accepts from several listeners as one (the bind address
// plus loopback, see Server.Listen). Addr is the first listener's.
type mergedListener struct {
	lns   []net.Listener
	conns chan net.Conn
	errc  chan error
	done  chan struct{}
	once  sync.Once
}

func newMergedListener(lns ...net.Listener) *mergedListener {
	m := &mergedListener{lns: lns, conns: make(chan net.Conn), errc: make(chan error, len(lns)), done: make(chan struct{})}
	for _, ln := range lns {
		go m.loop(ln)
	}
	return m
}

func (m *mergedListener) loop(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			select {
			case m.errc <- err:
			case <-m.done:
			}
			return
		}
		select {
		case m.conns <- c:
		case <-m.done:
			_ = c.Close()
			return
		}
	}
}

func (m *mergedListener) Accept() (net.Conn, error) {
	select {
	case c := <-m.conns:
		return c, nil
	case err := <-m.errc:
		return nil, err
	case <-m.done:
		return nil, net.ErrClosed
	}
}

func (m *mergedListener) Close() error {
	var first error
	m.once.Do(func() {
		close(m.done)
		for _, ln := range m.lns {
			if err := ln.Close(); err != nil && first == nil {
				first = err
			}
		}
	})
	return first
}

func (m *mergedListener) Addr() net.Addr { return m.lns[0].Addr() }
