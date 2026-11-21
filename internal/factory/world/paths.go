package world

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

func (g *Git) pathCommand(ctx context.Context, cwd, script string, paths ...string) (hc.ExecResult, error) {
	return g.run(ctx, cwd, "/bin/sh", append([]string{"-c", script, "sheepdog-project-path"}, paths...), nil)
}

// stat's C-locale diagnostic distinguishes absence from failed lookups on
// both BSD and GNU systems. BSD stat -L can fall back to lstat for a dangling
// link. Hold on those links instead of treating their target as a known path.
const probePath = `probe() {
	if diagnostic=$(LC_ALL=C stat -L "$1" 2>&1 >/dev/null); then
		if [ ! -L "$1" ] || [ -e "$1" ]; then return 0; fi
	else
		case "$diagnostic" in
			*": No such file or directory"|*": Not a directory")
				if [ ! -L "$1" ]; then return 1; fi;;
		esac
		printf '%s\n' "$diagnostic" >&2
	fi
	return 2
}
`

// PathExists probes a live path in the login session. Only a completed
// negative probe counts as absence.
func (g *Git) PathExists(ctx context.Context, cwd, path string) (bool, error) {
	path, err := absolutePath(path)
	if err != nil {
		return false, err
	}
	_, err = g.pathCommand(ctx, cwd, probePath+`probe "$1"`, path)
	if gitExit(err, 1) {
		return false, nil
	}
	return err == nil, err
}

// CanonicalPaths resolves paths, retaining any suffix below a
// missing directory. An empty path is retained as an empty path.
func (g *Git) CanonicalPaths(ctx context.Context, cwd string, paths ...string) (map[string]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	input := make([]string, len(paths))
	for i, path := range paths {
		if path != "" {
			var err error
			path, err = absolutePath(path)
			if err != nil {
				return nil, err
			}
		}
		input[i] = path
	}
	r, err := g.pathCommand(ctx, cwd, probePath+`for p do
	if [ -z "$p" ]; then printf '\0'; continue; fi
	tail=
	while probe "$p"; status=$?; [ "$status" = 1 ]; do
		tail=/${p##*/}$tail
		p=${p%/*}; p=${p:-/}
	done
	[ "$status" = 0 ] || exit 2
	real=$(realpath "$p" && printf .) || exit 2
	real=${real%??}
	if [ -n "$tail" ]; then real=${real%/}$tail; fi
	printf '%s\0' "$real"
done`, input...)
	if err != nil {
		return nil, err
	}
	out := strings.Split(string(r.Stdout), "\x00")
	if len(out) != len(paths)+1 || out[len(paths)] != "" {
		return nil, errors.New("unexpected project path response")
	}
	resolved := make(map[string]string, len(paths))
	for i, path := range paths {
		if path != "" && !filepath.IsAbs(out[i]) {
			return nil, errors.New("unexpected resolved project path")
		}
		resolved[path] = out[i]
	}
	return resolved, nil
}

// ErrProofOutputRejected means the path is unsuitable for a proof log.
var ErrProofOutputRejected = errors.New("invalid proof output path")

// ProofOutput reads the prefix of a regular proof log in the worktree's
// Git metadata. Proof logs are written by report ready and live outside HEAD.
func (g *Git) ProofOutput(ctx context.Context, worktree, path string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, ErrProofOutputRejected
	}
	r, err := g.pathCommand(ctx, worktree, probePath+`
dir=$(git rev-parse --absolute-git-dir && printf .) || exit 3
dir=$(realpath "${dir%??}" && printf .) || exit 3
dir=${dir%??}
[ ! -L "$1" ] || exit 2
probe "$1"
case $? in 1) exit 2;; 2) exit 3;; esac
path=$(realpath "$1" && printf .) || exit 3
[ -f "$1" ] || exit 2
path=${path%??}
case "$path" in "$dir"/*) head -c 4096 "$path" || exit 3; exit 0;; esac
exit 2`, path)
	if gitExit(err, 2) {
		return nil, ErrProofOutputRejected
	}
	if err != nil {
		return nil, err
	}
	return r.Stdout, nil
}
