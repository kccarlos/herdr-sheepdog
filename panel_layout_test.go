package herdrsheepdog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"testing"
)

func TestLayoutPanelFilesStayInSync(t *testing.T) {
	const syncFix = "run `make sync-layout`"
	embedded := map[string][]byte{}
	if err := fs.WalkDir(Panel, "mods/sheepdog", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(Panel, name)
		if err != nil {
			return err
		}
		embedded[name] = data
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	mirrorRoot := "sheepdog-layout/mods/sheepdog"
	mirrored := map[string]bool{}
	if err := fs.WalkDir(os.DirFS(mirrorRoot), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// herdr-app writes these into any mod folder it loads or checks;
		// .gitignore (the same as mods/sheepdog's) keeps them out of git.
		switch path.Base(rel) {
		case "gpui-kit.d.ts", "jsconfig.json", ".gitignore":
			return nil
		}
		name := "mods/sheepdog/" + rel
		mirrored[name] = true
		want, ok := embedded[name]
		if !ok {
			t.Errorf("%s is not embedded in Panel; %s", name, syncFix)
			return nil
		}
		got, err := os.ReadFile(mirrorRoot + "/" + rel)
		if err != nil {
			t.Errorf("read %s: %v; %s", name, err, syncFix)
		} else if !bytes.Equal(got, want) {
			t.Errorf("%s differs from the file embedded in Panel; %s", name, syncFix)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk %s: %v; %s", mirrorRoot, err, syncFix)
	}
	for name := range embedded {
		if !mirrored[name] {
			t.Errorf("%s is embedded in Panel but missing from its layout copy; %s", name, syncFix)
		}
	}
}

type layoutNode struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Size     layoutSize   `json:"size"`
	Min      int          `json:"min"`
	Max      int          `json:"max"`
	Content  layoutMod    `json:"content"`
	Children []layoutNode `json:"children"`
}

type layoutSize struct {
	Px int `json:"px"`
}

type layoutMod struct {
	Mod string `json:"mod"`
}

func TestSheepdogLayoutShape(t *testing.T) {
	var root layoutNode
	if err := json.Unmarshal(Layout, &root); err != nil {
		t.Fatalf("embedded sheepdog layout does not parse: %v", err)
	}
	if root.Type != "row" {
		t.Fatalf("layout root type = %q, want row", root.Type)
	}
	if len(root.Children) == 0 {
		t.Fatal("layout root has no children")
	}
	last := root.Children[len(root.Children)-1]
	if last.ID != "sheepdog" || last.Type != "block" || last.Size.Px != 340 ||
		last.Min != 44 || last.Max != 720 || last.Content.Mod != "sheepdog" {
		t.Fatalf("last root child = %+v, want the sheepdog block", last)
	}

	// These are the mods used by herdr-app 0.1.10's shipped main layout.
	shipped := map[string]bool{
		"workspace-chrome": true,
		"spaces":           true,
		"agents":           true,
		"tabs":             true,
		"pane":             true,
		"files":            true,
		"changes":          true,
		"workspace-status": true,
	}
	var checkMods func(layoutNode) error
	checkMods = func(node layoutNode) error {
		if mod := node.Content.Mod; mod != "" && mod != "sheepdog" && !shipped[mod] {
			return fmt.Errorf("layout node %q references unknown mod %q", node.ID, mod)
		}
		for _, child := range node.Children {
			if err := checkMods(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := checkMods(root); err != nil {
		t.Fatal(err)
	}
}
