package update

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Install upgrades this sheepdog to release tag the way it was installed
// (kind as `sheepdog update` reports it; exe the executable with symlinks
// resolved) and, on macOS, Sheepdog.app when Homebrew installed its cask.
// It asks nothing and does not restart the daemon. Commands' output goes
// to out.
func Install(ctx context.Context, kind, exe, tag string, out io.Writer) error {
	var err error
	switch kind {
	case "binary":
		err = InstallBinary(ctx, exe, tag)
	case "homebrew":
		if err = run(ctx, out, "brew", "update"); err == nil {
			err = run(ctx, out, "brew", "upgrade", "kccarlos/tap/sheepdog")
		}
	case "herdr plugin":
		err = run(ctx, out, "herdr", "plugin", "update", "--yes", "kccarlos.sheepdog")
	default:
		return fmt.Errorf("a %s install cannot be updated automatically", kind)
	}
	if err != nil {
		return err
	}
	const cask = "kccarlos/tap/sheepdog-app"
	if runtime.GOOS == "darwin" && run(ctx, io.Discard, "brew", "list", "--cask", cask) == nil {
		if err := run(ctx, out, "brew", "upgrade", "--cask", cask); err != nil {
			// The daemon is updated: say so and carry on to its restart.
			_, _ = fmt.Fprintf(out, "Sheepdog.app was not updated: %v\n", err)
		}
	}
	return nil
}

// command builds every brew and herdr command Install runs (tests check
// it cannot reach the real tools).
var command = exec.CommandContext

func run(ctx context.Context, out io.Writer, name string, args ...string) error {
	cmd := command(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

// InstallBinary replaces exe with release tag's build for this OS and
// architecture: the archive and checksums.txt come from that tag's
// download directory (SHEEPDOG_RELEASE_URL replaces it, as for
// scripts/install.sh), the sha256 must match, and the new binary must
// report tag before it is renamed over exe.
func InstallBinary(ctx context.Context, exe, tag string) error {
	base := os.Getenv("SHEEPDOG_RELEASE_URL")
	if base == "" {
		base = "https://github.com/" + Repo + "/releases/download/" + tag
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	asset := "sheepdog_" + tag + "_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
	sums, err := download(ctx, base+"/checksums.txt", 1<<20)
	if err != nil {
		return err
	}
	want := ""
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) == 2 && strings.TrimPrefix(f[1], "*") == asset {
			want = f[0]
			break
		}
	}
	if want == "" {
		return fmt.Errorf("release %s has no %s build (%s)", tag, runtime.GOOS+"_"+runtime.GOARCH, asset)
	}
	archive, err := download(ctx, base+"/"+asset, 512<<20)
	if err != nil {
		return err
	}
	if sum := sha256.Sum256(archive); hex.EncodeToString(sum[:]) != want {
		return fmt.Errorf("%s does not match checksums.txt; not installing", asset)
	}
	bin, err := untarSheepdog(archive)
	if err != nil {
		return fmt.Errorf("%s: %w", asset, err)
	}
	f, err := os.CreateTemp(filepath.Dir(exe), ".sheepdog.*")
	if err != nil {
		return err
	}
	staged := f.Name()
	defer func() { _ = os.Remove(staged) }() // gone after the rename
	_, err = f.Write(bin)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(staged, 0o755)
	}
	if err != nil {
		return err
	}
	line, err := ProbeVersion(ctx, staged)
	if err != nil || VersionOf(line) != tag {
		return fmt.Errorf("the downloaded sheepdog does not start as %s (%q, %v); not installing", tag, line, err)
	}
	return os.Rename(staged, exe)
}

func download(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("could not download %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// untarSheepdog is the sheepdog file in a release archive.
func untarSheepdog(archive []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("no sheepdog in the archive")
		}
		if err != nil {
			return nil, err
		}
		if h.Name == "sheepdog" && h.Typeflag == tar.TypeReg {
			return io.ReadAll(io.LimitReader(tr, 512<<20))
		}
	}
}
