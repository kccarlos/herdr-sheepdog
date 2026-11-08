package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeRelease serves tag's archive (whose sheepdog prints `reports`) and
// checksums.txt (with sum, or the archive's real sha256 when sum is "").
func fakeRelease(t *testing.T, tag, reports, sum string) {
	t.Helper()
	script := "#!/bin/sh\necho 'sheepdog " + reports + " (commit x, built y)'\n"
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	_ = tw.WriteHeader(&tar.Header{Name: "sheepdog", Mode: 0o755, Size: int64(len(script)), Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte(script))
	_ = tw.Close()
	_ = zw.Close()
	asset := "sheepdog_" + tag + "_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
	if sum == "" {
		s := sha256.Sum256(buf.Bytes())
		sum = hex.EncodeToString(s[:])
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/download/" + tag + "/checksums.txt":
			_, _ = io.WriteString(w, "0000  sheepdog_"+tag+"_other_arch.tar.gz\n"+sum+"  *"+asset+"\n")
		case "/download/" + tag + "/" + asset:
			_, _ = w.Write(buf.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("SHEEPDOG_RELEASE_URL", srv.URL+"/download/"+tag)
}

func oldBinary(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "sheepdog")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	es, _ := os.ReadDir(dir)
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	return names
}

func TestInstallBinaryVerifiesAndReplaces(t *testing.T) {
	log := fakeTools(t, false) // never the real brew
	fakeRelease(t, "v9.9.9", "v9.9.9", "")
	exe := oldBinary(t)
	if err := Install(context.Background(), "binary", exe, "v9.9.9", io.Discard); err != nil {
		t.Fatal(err)
	}
	line, err := ProbeVersion(context.Background(), exe)
	if err != nil || VersionOf(line) != "v9.9.9" {
		t.Errorf("installed binary reports %q, %v", line, err)
	}
	if got := dirEntries(t, filepath.Dir(exe)); len(got) != 1 {
		t.Errorf("left files behind: %v", got)
	}
	if b, _ := os.ReadFile(log); len(b) != 0 {
		t.Errorf("ran %s without Sheepdog.app installed", b)
	}
}

func TestInstallBinaryRefusesABadDownload(t *testing.T) {
	for name, c := range map[string]struct{ reports, sum, want string }{
		"checksum":      {"v9.9.9", strings.Repeat("ab", 32), "does not match checksums.txt"},
		"wrong version": {"v9.9.8", "", "does not start as v9.9.9"},
	} {
		t.Run(name, func(t *testing.T) {
			fakeRelease(t, "v9.9.9", c.reports, c.sum)
			exe := oldBinary(t)
			err := InstallBinary(context.Background(), exe, "v9.9.9")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
			if b, _ := os.ReadFile(exe); string(b) != "old" {
				t.Error("the old binary was replaced")
			}
			if got := dirEntries(t, filepath.Dir(exe)); len(got) != 1 {
				t.Errorf("left files behind: %v", got)
			}
		})
	}
	fakeRelease(t, "v9.9.9", "v9.9.9", "")
	if err := InstallBinary(context.Background(), oldBinary(t), "v1.0.0"); err == nil {
		t.Error("a tag without assets installed")
	}
}

// fakeTools puts brew and herdr on PATH that log their arguments; brew
// list --cask succeeds when caskInstalled, brew upgrade --cask fails when
// FAKE_CASK_FAILS is set.
func fakeTools(t *testing.T, caskInstalled bool) (log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "log")
	list := "exit 1"
	if caskInstalled {
		list = "exit 0"
	}
	for _, name := range []string{"brew", "herdr"} {
		script := "#!/bin/sh\nif [ \"$1 $2\" = 'list --cask' ]; then " + list + "; fi\necho " + name + " \"$@\" >> " + log + "\n" +
			"if [ \"$1 $2\" = 'upgrade --cask' ] && [ -n \"$FAKE_CASK_FAILS\" ]; then exit 1; fi\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	return log
}

func TestInstallRunsTheInstallersTools(t *testing.T) {
	for _, c := range []struct {
		kind string
		cask bool
		want string
	}{
		{"homebrew", false, "brew update\nbrew upgrade kccarlos/tap/sheepdog\n"},
		{"herdr plugin", false, "herdr plugin update --yes kccarlos.sheepdog\n"},
		{"herdr plugin", true, "herdr plugin update --yes kccarlos.sheepdog\n"},
	} {
		log := fakeTools(t, c.cask)
		if err := Install(context.Background(), c.kind, "/x/sheepdog", "v9.9.9", io.Discard); err != nil {
			t.Fatalf("%s: %v", c.kind, err)
		}
		want := c.want
		if c.cask && runtime.GOOS == "darwin" {
			want += "brew upgrade --cask kccarlos/tap/sheepdog-app\n"
		}
		if b, _ := os.ReadFile(log); string(b) != want {
			t.Errorf("%s (cask %v) ran:\n%s\nwant:\n%s", c.kind, c.cask, b, want)
		}
	}
	if err := Install(context.Background(), "source", "/x/sheepdog", "v9.9.9", io.Discard); err == nil {
		t.Error("a source checkout was installed")
	}
	if runtime.GOOS == "darwin" {
		// A failed Sheepdog.app upgrade is reported, not an error: the
		// daemon is updated and must still be restarted.
		fakeTools(t, true)
		t.Setenv("FAKE_CASK_FAILS", "1")
		var out bytes.Buffer
		if err := Install(context.Background(), "herdr plugin", "/x/sheepdog", "v9.9.9", &out); err != nil || !strings.Contains(out.String(), "Sheepdog.app was not updated") {
			t.Errorf("cask failure: %v, %q", err, out.String())
		}
	}
}
