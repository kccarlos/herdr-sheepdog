package docscheck

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestSurfaceProtocolEnumParity(t *testing.T) {
	root := repoRoot(t)

	cardKinds := parseGoStringConsts(t, filepath.Join(root, "internal/voice/wsproto/wsproto.go"), "CardKind")
	if len(cardKinds) == 0 {
		t.Fatal("found no CardKind* constants in internal/voice/wsproto/wsproto.go")
	}
	problems := parseGoStringConsts(t, filepath.Join(root, "internal/brain/account.go"), "Problem")
	if len(problems) == 0 {
		t.Fatal("found no Problem* constants in internal/brain/account.go")
	}

	// Verify ws-contract.md documents every CardKind and AccountProblem value.
	wsContractBytes, err := os.ReadFile(filepath.Join(root, "docs/dev/ws-contract.md"))
	if err != nil {
		t.Fatal(err)
	}
	wsContract := string(wsContractBytes)
	for _, k := range cardKinds {
		if !strings.Contains(wsContract, "`"+k+"`") && !strings.Contains(wsContract, `"`+k+`"`) {
			t.Errorf("docs/dev/ws-contract.md does not document CardKind %q", k)
		}
	}
	for _, p := range problems {
		if !strings.Contains(wsContract, "`"+p+"`") && !strings.Contains(wsContract, `"`+p+`"`) {
			t.Errorf("docs/dev/ws-contract.md does not document AccountProblem %q", p)
		}
	}

	// Verify every emitted daemon event family is reachable through /events SSE.
	sseKinds := parseSSEEventKinds(t, filepath.Join(root, "internal/web/sse.go"))
	emitted := collectDaemonEmittedEvents(t, root)
	if len(emitted) == 0 {
		t.Fatal("found no emitted daemon events")
	}
	var emittedSorted []string
	for ev := range emitted {
		emittedSorted = append(emittedSorted, ev)
	}
	sort.Strings(emittedSorted)
	for _, ev := range emittedSorted {
		kind, _, _ := strings.Cut(ev, ".")
		if !sseKinds[kind] {
			t.Errorf("daemon emits %q, whose prefix %q is missing from internal/web/sse.go eventKinds", ev, kind)
		}
	}
}

// parseGoStringConsts returns non-empty string constant values in path whose
// identifier starts with prefix (e.g. CardKind* or Problem*).
func parseGoStringConsts(t testing.TB, path, prefix string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var out []string
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, prefix) || i >= len(vs.Values) {
					continue
				}
				bl, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || bl.Kind != token.STRING {
					continue
				}
				val, err := strconv.Unquote(bl.Value)
				if err == nil && val != "" {
					out = append(out, val)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

func parseSSEEventKinds(t testing.TB, path string) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	out := map[string]bool{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) == 0 || vs.Names[0].Name != "eventKinds" || len(vs.Values) == 0 {
				continue
			}
			cl, ok := vs.Values[0].(*ast.CompositeLit)
			if !ok {
				continue
			}
			for _, elt := range cl.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				bl, ok := kv.Key.(*ast.BasicLit)
				if !ok || bl.Kind != token.STRING {
					continue
				}
				if k, err := strconv.Unquote(bl.Value); err == nil {
					out[k] = true
				}
			}
		}
	}
	if len(out) == 0 {
		t.Fatalf("found no eventKinds in %s", path)
	}
	return out
}

var validEventNameRe = regexp.MustCompile(`^[a-z][a-z_]*(?:\.[a-z][a-z_]*)+$`)

func collectDaemonEmittedEvents(t testing.TB, root string) map[string]bool {
	t.Helper()
	emitted := map[string]bool{}
	consts := map[string]string{}
	fset := token.NewFileSet()
	for _, sub := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, sub), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			pkg := file.Name.Name
			for _, decl := range file.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok || gen.Tok != token.CONST {
					continue
				}
				for _, spec := range gen.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for i, name := range vs.Names {
						if i >= len(vs.Values) {
							continue
						}
						if bl, ok := vs.Values[i].(*ast.BasicLit); ok && bl.Kind == token.STRING {
							if v, err := strconv.Unquote(bl.Value); err == nil && validEventNameRe.MatchString(v) {
								consts[pkg+"."+name.Name] = v
								consts[name.Name] = v
							}
						}
					}
				}
			}
			scanScope := func(rootNode ast.Node) {
				varEvents := map[string][]string{}
				ast.Inspect(rootNode, func(n ast.Node) bool {
					assign, ok := n.(*ast.AssignStmt)
					if !ok {
						return true
					}
					for i, lhs := range assign.Lhs {
						if i >= len(assign.Rhs) {
							break
						}
						id, ok := lhs.(*ast.Ident)
						if !ok || id.Name == "_" {
							continue
						}
						bl, ok := assign.Rhs[i].(*ast.BasicLit)
						if !ok || bl.Kind != token.STRING {
							continue
						}
						if v, err := strconv.Unquote(bl.Value); err == nil && validEventNameRe.MatchString(v) {
							varEvents[id.Name] = append(varEvents[id.Name], v)
						}
					}
					return true
				})
				ast.Inspect(rootNode, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok || len(call.Args) < 2 {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != "Log" {
						return true
					}
					switch arg := call.Args[1].(type) {
					case *ast.BasicLit:
						if arg.Kind == token.STRING {
							if v, err := strconv.Unquote(arg.Value); err == nil && validEventNameRe.MatchString(v) {
								emitted[v] = true
							}
						}
					case *ast.SelectorExpr:
						if id, ok := arg.X.(*ast.Ident); ok {
							if v, ok := consts[id.Name+"."+arg.Sel.Name]; ok {
								emitted[v] = true
							}
						}
					case *ast.Ident:
						if v, ok := consts[pkg+"."+arg.Name]; ok {
							emitted[v] = true
						}
						for _, v := range varEvents[arg.Name] {
							emitted[v] = true
						}
					}
					return true
				})
			}
			for _, decl := range file.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
					scanScope(fn.Body)
				} else {
					scanScope(decl)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return emitted
}
