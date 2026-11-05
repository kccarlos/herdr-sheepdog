// Package files is the read-only file browser's view of one folder
// (docs/dev/design/file-browser.md): list, search and read paths relative
// to a root the daemon chose. Nothing here writes a file.
//
// The rails are in code, here:
//   - a client's path is clean and relative: no "..", no leading "/";
//   - every open goes through os.Root, which refuses ".." and symlinks that
//     leave the root, including one swapped in between a check and the open;
//   - sensitive names (Sensitive) are not listed, not searched and not
//     read, on any segment a path passes through, a symlink's target too;
//   - only regular files are read: the type comes from the open handle's
//     stat, and the open does not block, so a FIFO cannot hang a read.
//
// The sensitive rule guards against the likely case (a .env in the folder,
// a link to it); it is not a promise against an agent racing the phone.
package files

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// The errors every call returns; the web layer maps each to a status and
// a reason (docs/dev/ws-contract.md "Files").
var (
	ErrBadPath     = errors.New("not a clean relative path")
	ErrOutside     = errors.New("that leaves the folder")
	ErrSensitive   = errors.New("hidden for safety")
	ErrHomeRoot    = errors.New("home folder cannot be browsed")
	ErrPrivateRoot = errors.New("private folder cannot be browsed")
	ErrNotFound    = errors.New("not found")
	ErrNotFile     = errors.New("not a regular file")
	ErrDenied      = errors.New("the system refused")
	ErrTooLarge    = errors.New("too large to show")
	ErrNotImage    = errors.New("not an image")
	ErrFailed      = errors.New("could not be read")
)

// maxLinks bounds the symlinks one path may pass through.
const maxLinks = 40

// Root is one folder the browser serves.
type Root struct {
	root *os.Root
	name string
}

// CheckRoot refuses roots that should never be browsed as a whole:
//   - the user's home folder or any folder above it (ErrHomeRoot);
//   - any folder in privateDirs, its parents, or anything inside it
//     (ErrPrivateRoot), e.g. the daemon's data directory;
//   - a folder with a sensitive segment anywhere on its cleaned or
//     symlink-resolved path (ErrSensitive).
func CheckRoot(dir string, privateDirs ...string) error {
	root := resolvePath(dir)
	if home, err := os.UserHomeDir(); err == nil && pathContains(root, resolvePath(home)) {
		return ErrHomeRoot
	}
	for _, p := range privateDirs {
		rp := resolvePath(p)
		if pathContains(root, rp) || pathContains(rp, root) {
			return ErrPrivateRoot
		}
	}
	if SensitivePath(filepath.ToSlash(filepath.Clean(dir))) || SensitivePath(filepath.ToSlash(root)) {
		return ErrSensitive
	}
	return nil
}

// resolvePath makes p absolute and resolves symlinks on p (or on its deepest
// existing prefix when p does not exist yet, e.g. /var -> /private/var).
func resolvePath(p string) string {
	if p == "" {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	p = filepath.Clean(p)
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	var tail []string
	cur := p
	for {
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
		if real, err := filepath.EvalSymlinks(cur); err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				real = filepath.Join(real, tail[i])
			}
			return real
		}
	}
}

// pathContains reports whether child is parent or inside parent.
func pathContains(parent, child string) bool {
	if parent == "" || child == "" {
		return false
	}
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Open opens dir as a root. A missing folder, or a path that is not a
// folder, is ErrNotFound; the home folder (or an ancestor) is ErrHomeRoot;
// a folder with a sensitive name anywhere on its path (an agent working in
// .git or ~/.ssh), spelled or linked, is ErrSensitive.
func Open(dir string) (*Root, error) {
	if err := CheckRoot(dir); err != nil {
		return nil, err
	}
	// Stat first: os.OpenRoot's own open would wait on a FIFO.
	info, err := os.Stat(dir)
	if err != nil {
		return nil, osErr(err)
	}
	if !info.IsDir() {
		return nil, ErrNotFound
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, osErr(err)
	}
	return &Root{root: r, name: filepath.Base(dir)}, nil
}

// Close releases the root.
func (r *Root) Close() error { return r.root.Close() }

// clean checks a client's path and splits it: "" (or ".") is the root
// itself; anything else is slash-separated and relative, with no empty,
// "." or ".." segment.
func clean(rel string) ([]string, error) {
	if rel == "" || rel == "." {
		return nil, nil
	}
	if strings.HasPrefix(rel, "/") || filepath.IsAbs(rel) || strings.ContainsRune(rel, 0) {
		return nil, ErrBadPath
	}
	parts := strings.Split(rel, "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return nil, ErrBadPath
		}
	}
	return parts, nil
}

// resolve checks rel and follows it inside the root one segment at a time,
// symlinks included, so the sensitive rule sees every name the path really
// passes through: a link "notes" to ".env" is ".env". It returns the path
// with no symlink left in it ("." for the root). A link whose target is
// absolute or climbs above the root is ErrOutside, as os.Root has it.
func (r *Root) resolve(rel string) (string, error) {
	todo, err := clean(rel)
	if err != nil {
		return "", err
	}
	var done []string
	links := 0
	for len(todo) > 0 {
		name := todo[0]
		todo = todo[1:]
		switch name {
		case "", ".":
			continue
		case "..":
			if len(done) == 0 {
				return "", ErrOutside
			}
			done = done[:len(done)-1]
			continue
		}
		if Sensitive(name) {
			return "", ErrSensitive
		}
		p := path.Join(path.Join(done...), name)
		info, err := r.root.Lstat(p)
		if err != nil {
			return "", osErr(err)
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			done = append(done, name)
			continue
		}
		if links++; links > maxLinks {
			return "", fmt.Errorf("%w: too many links", ErrDenied)
		}
		target, err := r.root.Readlink(p)
		if err != nil {
			return "", osErr(err)
		}
		if filepath.IsAbs(target) || path.IsAbs(filepath.ToSlash(target)) {
			return "", ErrOutside
		}
		todo = append(strings.Split(filepath.ToSlash(target), "/"), todo...)
	}
	if len(done) == 0 {
		return ".", nil
	}
	return path.Join(done...), nil
}

// openAt opens a resolved path for reading without blocking (a FIFO's
// open waits for a writer otherwise) and stats the open handle, so the
// type is the type of what was opened.
func (r *Root) openAt(p string) (*os.File, fs.FileInfo, error) {
	f, err := r.root.OpenFile(p, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, osErr(err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, osErr(err)
	}
	return f, info, nil
}

// osErr maps a system error to one of the package's. The result never
// carries the system's own text, which names a path on the computer: a
// known error gets a fixed reason, any other is ErrFailed.
func osErr(err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
		return ErrNotFound
	case errors.Is(err, fs.ErrPermission), errors.Is(err, syscall.ELOOP):
		return ErrDenied
	case errors.Is(err, syscall.ENXIO), errors.Is(err, syscall.EOPNOTSUPP), errors.Is(err, syscall.ENODEV):
		// The open of a socket, a device or a FIFO nobody writes.
		return ErrNotFile
	}
	return ErrFailed
}

// modified is a file's time as the API shows it: UTC, whole seconds.
func modified(info fs.FileInfo) time.Time {
	return info.ModTime().UTC().Truncate(time.Second)
}
