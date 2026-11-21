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

// PathExists probes a live path in the login session. Only a completed
// negative probe counts as absence.
func (g *Git) PathExists(ctx context.Context, cwd, path string) (bool, error) {
	if !filepath.IsAbs(path) {
		abs, err := absolutePath(path)
		if err != nil {
			return false, err
		}
		path = abs
	}
	_, err := g.pathCommand(ctx, cwd, `[ -e "$1" ] && exit 0
p=$1
while [ ! -e "$p" ] && [ ! -L "$p" ]; do
	p=${p%/*}; p=${p:-/}
done
if [ -d "$p" ] && [ ! -x "$p" ]; then exit 2; fi
exit 1`, path)
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
		if path != "" && !filepath.IsAbs(path) {
			var err error
			path, err = absolutePath(path)
			if err != nil {
				return nil, err
			}
		}
		input[i] = path
	}
	r, err := g.pathCommand(ctx, cwd, `for p do
	if [ -z "$p" ]; then printf '\0'; continue; fi
	tail=
	while [ ! -e "$p" ] && [ ! -L "$p" ]; do
		tail=/${p##*/}$tail
		p=${p%/*}; p=${p:-/}
	done
	if [ -n "$tail" ] && [ -d "$p" ] && [ ! -x "$p" ]; then exit 2; fi
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

// ProofOutput reads the prefix of a regular proof log in the worktree's
// Git metadata. Proof logs are written by report ready and live outside HEAD.
func (g *Git) ProofOutput(ctx context.Context, worktree, path string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("proof output path must be absolute")
	}
	r, err := g.pathCommand(ctx, worktree, `
dir=$(git rev-parse --absolute-git-dir && printf .) || exit 2
dir=$(realpath "${dir%??}" && printf .) || exit 2
dir=${dir%??}
[ -f "$1" ] && [ ! -L "$1" ] || exit 2
path=$(realpath "$1" && printf .) || exit 2
path=${path%??}
case "$path" in "$dir"/*) exec head -c 4096 "$path";; esac
exit 2`, path)
	if err != nil {
		return nil, err
	}
	return r.Stdout[:min(len(r.Stdout), 4096)], nil
}
