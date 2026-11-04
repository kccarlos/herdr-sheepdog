package files

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
	"time"
)

// The list and search limits (docs/dev/design/file-browser.md, section 3).
const (
	// ListMax caps the entries of one list.
	ListMax = 2000
	// ListReadMax caps the names read from one folder, so a huge folder
	// costs a bounded amount; past it the list is truncated.
	ListReadMax = 20000
	// SearchSeenMax, SearchMatchMax and SearchTimeout bound one search.
	SearchSeenMax  = 20000
	SearchMatchMax = 200
	SearchTimeout  = 2 * time.Second
	// dirBatch is how many names one folder read asks for.
	dirBatch = 256
)

// The kinds of a listed entry.
const (
	KindFile = "file"
	KindDir  = "dir"
	KindLink = "link"
)

// Entry is one listed file, folder or link.
type Entry struct {
	Name string `json:"name"`
	// Path is relative to the root, what a read or a list takes.
	Path string `json:"path"`
	Kind string `json:"kind"`
	// Size is a file's; 0 for a folder or a link.
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	// Blocked marks a link that leaves the root.
	Blocked bool `json:"blocked,omitempty"`
}

// Listing is a folder's list or a search's matches.
type Listing struct {
	RootName  string `json:"root_name"`
	Path      string `json:"path"`
	Truncated bool   `json:"truncated"`
	// Hidden counts what the sensitive rule left out.
	Hidden  int     `json:"hidden"`
	Entries []Entry `json:"entries"`
}

// List lists the folder at rel, folders first, then by name, at most
// ListMax entries. Sensitive names are left out and counted in Hidden;
// FIFOs, sockets and devices are left out. When ctx ends, it answers what
// it has with Truncated set.
func (r *Root) List(ctx context.Context, rel string) (Listing, error) {
	dir, f, err := r.openDir(rel)
	if err != nil {
		return Listing{}, err
	}
	defer func() { _ = f.Close() }()
	out := Listing{RootName: r.name, Path: shown(rel), Entries: []Entry{}}
	var ents []fs.DirEntry
	seen := 0
	err = eachEntry(f, func(d fs.DirEntry) bool {
		if seen++; seen > ListReadMax || ctx.Err() != nil {
			out.Truncated = true
			return false
		}
		switch {
		case Sensitive(d.Name()):
			out.Hidden++
		case listable(d.Type()):
			ents = append(ents, d)
		}
		return true
	})
	if err != nil {
		return Listing{}, err
	}
	slices.SortFunc(ents, func(a, b fs.DirEntry) int { return compareEntries(a.IsDir(), a.Name(), b.IsDir(), b.Name()) })
	if len(ents) > ListMax {
		ents, out.Truncated = ents[:ListMax], true
	}
	for _, d := range ents {
		// Each link costs a walk of its chain.
		if ctx.Err() != nil {
			out.Truncated = true
			break
		}
		if e, ok := r.entry(out.Path, dir, d.Name()); ok {
			out.Entries = append(out.Entries, e)
		}
	}
	return out, nil
}

// Search finds the names under the folder rel that contain q, ignoring
// case, walking shallowest first and answering in List's order. It does
// not follow links, skips node_modules and
// never matches a sensitive name or looks under one (one that would have
// matched counts in Hidden). It stops at SearchSeenMax entries seen,
// SearchMatchMax matches or SearchTimeout, with Truncated set.
func (r *Root) Search(ctx context.Context, rel, q string) (Listing, error) {
	ctx, cancel := context.WithTimeout(ctx, SearchTimeout)
	defer cancel()
	dir, f, err := r.openDir(rel)
	if err != nil {
		return Listing{}, err
	}
	out := Listing{RootName: r.name, Path: shown(rel), Entries: []Entry{}}
	needle := fold(q)
	type folder struct{ shown, real string }
	var queue []folder
	seen := 0
	// done says a limit is reached; the walk then stops, truncated.
	done := func() bool {
		if seen >= SearchSeenMax || len(out.Entries) >= SearchMatchMax || ctx.Err() != nil {
			out.Truncated = true
		}
		return out.Truncated
	}
	scan := func(f *os.File, at folder) error {
		defer func() { _ = f.Close() }()
		return eachEntry(f, func(d fs.DirEntry) bool {
			if done() {
				return false
			}
			seen++
			name := d.Name()
			folded := fold(name)
			match := strings.Contains(folded, needle)
			switch {
			case Sensitive(name):
				if match {
					out.Hidden++
				}
				return true
			case folded == nodeModules || !listable(d.Type()):
				return true
			}
			if match {
				if e, ok := r.entry(at.shown, at.real, name); ok {
					out.Entries = append(out.Entries, e)
				}
			}
			if d.IsDir() {
				queue = append(queue, folder{path.Join(at.shown, name), path.Join(at.real, name)})
			}
			return true
		})
	}
	if err := scan(f, folder{out.Path, dir}); err != nil {
		return Listing{}, err
	}
	for len(queue) > 0 && !done() {
		at := queue[0]
		queue = queue[1:]
		f, info, err := r.openAt(at.real)
		if err != nil {
			continue
		}
		if !info.IsDir() {
			_ = f.Close()
			continue
		}
		// A folder that cannot be read is skipped.
		_ = scan(f, at)
	}
	slices.SortFunc(out.Entries, func(a, b Entry) int {
		if c := compareEntries(a.Kind == KindDir, a.Name, b.Kind == KindDir, b.Name); c != 0 {
			return c
		}
		return strings.Compare(a.Path, b.Path)
	})
	return out, nil
}

// openDir resolves rel and opens it as a folder.
func (r *Root) openDir(rel string) (string, *os.File, error) {
	p, err := r.resolve(rel)
	if err != nil {
		return "", nil, err
	}
	f, info, err := r.openAt(p)
	if err != nil {
		return "", nil, err
	}
	if !info.IsDir() {
		_ = f.Close()
		return "", nil, ErrNotFound
	}
	return p, f, nil
}

// eachEntry calls fn for each entry of an open folder, in the folder's
// order, until fn returns false.
func eachEntry(f *os.File, fn func(fs.DirEntry) bool) error {
	for {
		batch, err := f.ReadDir(dirBatch)
		for _, d := range batch {
			if !fn(d) {
				return nil
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return osErr(err)
		}
	}
}

// entry describes name in the resolved folder dir, listed under the
// client's path at. It is false for an entry gone since the folder was
// read, or one that is not a file, a folder or a link.
func (r *Root) entry(at, dir, name string) (Entry, bool) {
	p := path.Join(dir, name)
	info, err := r.root.Lstat(p)
	if err != nil {
		return Entry{}, false
	}
	e := Entry{Name: name, Path: path.Join(at, name), Modified: modified(info)}
	switch mode := info.Mode(); {
	case mode.IsDir():
		e.Kind = KindDir
	case mode.IsRegular():
		e.Kind, e.Size = KindFile, info.Size()
	case mode&fs.ModeSymlink != 0:
		_, err := r.resolve(p)
		e.Kind, e.Blocked = KindLink, errors.Is(err, ErrOutside)
	default:
		return Entry{}, false
	}
	return e, true
}

// listable says a directory entry's type is shown: a file, a folder or a
// link (not a FIFO, a socket or a device).
func listable(t fs.FileMode) bool {
	return t.IsRegular() || t.IsDir() || t&fs.ModeSymlink != 0
}

// compareEntries orders folders first, then by name, ignoring case.
func compareEntries(aDir bool, a string, bDir bool, b string) int {
	if aDir != bDir {
		if aDir {
			return -1
		}
		return 1
	}
	if c := strings.Compare(strings.ToLower(a), strings.ToLower(b)); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}

// shown is the folder's path as a list names it: "" for the root.
func shown(rel string) string {
	if rel == "." {
		return ""
	}
	return rel
}
