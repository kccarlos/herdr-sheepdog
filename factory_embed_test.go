package herdrsheepdog

import (
	"bytes"
	"io/fs"
	"os"
	"path"
	"regexp"
	"strings"
	"testing"
)

var (
	pyVersionRe        = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	pyFromDotModImport = regexp.MustCompile(`(?m)^\s*from\s+\.([A-Za-z_][A-Za-z0-9_]*)\s+import\b`)
	pyFromDotImportMod = regexp.MustCompile(`(?m)^\s*from\s+\.\s+import\s+([A-Za-z_][A-Za-z0-9_]*)`)
	pyFromPkgSubImport = regexp.MustCompile(`(?m)^\s*from\s+herdr_task\.([A-Za-z_][A-Za-z0-9_]*)\s+import\b`)
	pyFromPkgImportMod = regexp.MustCompile(`(?m)^\s*from\s+herdr_task\s+import\s+([A-Za-z_][A-Za-z0-9_]*)`)
)

func TestFactoryPythonVersion(t *testing.T) {
	got := FactoryPythonVersion()
	if !pyVersionRe.MatchString(got) {
		t.Fatalf("FactoryPythonVersion() = %q, want X.Y.Z", got)
	}
	disk, err := os.ReadFile("factory/.python-version")
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.TrimSpace(string(disk)); got != want {
		t.Fatalf("FactoryPythonVersion() = %q, want %q", got, want)
	}
}

func TestFactoryEmbedMatchesRuntimeTree(t *testing.T) {
	embedded := map[string][]byte{}
	if err := fs.WalkDir(Factory, "factory", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(Factory, p)
		if err != nil {
			return err
		}
		embedded[p] = b
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	onDisk := map[string]bool{}
	if err := fs.WalkDir(os.DirFS("factory"), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch {
		case rel == ".":
			return nil
		case rel == "test" || rel == "core" || rel == "contracts" || path.Base(rel) == "__pycache__":
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		case d.IsDir():
			return nil
		case path.Base(rel) == ".DS_Store" || strings.HasSuffix(rel, ".pyc"):
			return nil
		}
		name := "factory/" + rel
		onDisk[name] = true
		want, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		got, ok := embedded[name]
		if !ok {
			t.Errorf("runtime file %s is missing from Factory embed", name)
			return nil
		}
		if !bytes.Equal(got, want) {
			t.Errorf("embedded %s differs from disk", name)
		}
		info, err := os.Stat(name)
		if err != nil {
			return err
		}
		diskExec := info.Mode()&0o111 != 0
		shebangExec := bytes.HasPrefix(got, []byte("#!"))
		if diskExec != shebangExec {
			t.Errorf("%s: disk executable=%v but shebang=%v (unpacker uses #! to set 0755)", name, diskExec, shebangExec)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	for name := range embedded {
		if !onDisk[name] {
			t.Errorf("Factory unexpectedly embeds non-runtime file %s", name)
		}
	}

	for _, must := range []string{
		"factory/.python-version",
		"factory/NOTICE",
		"factory/bin/herdr-secret",
		"factory/mods/tasks/bin/herdr-task",
		"factory/mods/tasks/bin/herdr_task/__init__.py",
		"factory/mods/tasks/bin/herdr_task/reviewer.sb",
		"factory/templates/assistant.md",
		"factory/templates/reviewer.md",
		"factory/templates/worker.md",
	} {
		if _, ok := embedded[must]; !ok {
			t.Errorf("required runtime file %s is missing from Factory embed", must)
		}
	}
}

func TestFactoryEmbedsLocalPythonImports(t *testing.T) {
	seen := map[string]bool{}
	queue := []string{
		"factory/mods/tasks/bin/herdr-task",
		"factory/mods/tasks/bin/herdr_task/__init__.py",
	}
	for len(queue) > 0 {
		file := queue[0]
		queue = queue[1:]
		if seen[file] {
			continue
		}
		seen[file] = true
		data, err := fs.ReadFile(Factory, file)
		if err != nil {
			t.Fatalf("python import target %s is not embedded: %v", file, err)
		}
		src := string(data)
		for _, re := range []*regexp.Regexp{
			pyFromDotModImport,
			pyFromDotImportMod,
			pyFromPkgSubImport,
			pyFromPkgImportMod,
		} {
			for _, m := range re.FindAllStringSubmatch(src, -1) {
				target := "factory/mods/tasks/bin/herdr_task/" + m[1] + ".py"
				queue = append(queue, target)
			}
		}
	}
	if len(seen) < 10 {
		t.Fatalf("walked only %d embedded python files; import checker did not traverse herdr_task", len(seen))
	}
}
