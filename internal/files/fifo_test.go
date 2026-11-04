//go:build unix

package files

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"
)

// A FIFO is never read and never hangs: not listed, not searched, a read
// or a list of it refused at once, and a root that is one refused too.
func TestFIFO(t *testing.T) {
	r, dir := build(t, tree{files: map[string]string{"a.txt": "a"}, links: map[string]string{"to-pipe": "pipe"}})
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, p := range []string{"pipe", "to-pipe"} {
			if _, err := r.Read(p); !errors.Is(err, ErrNotFile) {
				t.Errorf("Read(%s) = %v, want ErrNotFile", p, err)
			}
			if _, _, _, err := r.Image(p); !errors.Is(err, ErrNotFile) {
				t.Errorf("Image(%s) = %v, want ErrNotFile", p, err)
			}
			if _, err := r.List(p); !errors.Is(err, ErrNotFound) {
				t.Errorf("List(%s) = %v, want ErrNotFound", p, err)
			}
		}
		if l, err := r.List(""); err != nil || !slices.Equal(names(l), []string{"a.txt", "to-pipe"}) {
			t.Errorf("List = %v %v", names(l), err)
		}
		if l, err := r.Search(context.Background(), "", "pipe"); err != nil || !slices.Equal(names(l), []string{"to-pipe"}) {
			t.Errorf("Search = %v %v", names(l), err)
		}
		if _, err := Open(filepath.Join(dir, "pipe")); !errors.Is(err, ErrNotFound) {
			t.Errorf("Open(pipe) = %v", err)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a FIFO hung the browser")
	}
}
