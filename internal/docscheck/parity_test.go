package docscheck

import (
	"fmt"
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

// surfaceID names each client surface that speaks the daemon's /ws and /events
// contracts. Keep this check strictly scoped to protocol-level enums (card
// kinds, account problem codes, and /events event types), never UI layout.
type surfaceID string

const (
	surfaceIOS   surfaceID = "ios"   // ios/Sources/{SheepdogKit,Sheepdog}
	surfaceMacOS surfaceID = "macos" // ios/Sources/SheepdogMac (+ SheepdogKit)
	surfacePanel surfaceID = "panel" // mods/sheepdog
	surfaceWeb   surfaceID = "web"   // web/voice
)

var allSurfaces = []surfaceID{surfaceIOS, surfaceMacOS, surfacePanel, surfaceWeb}

// surfaceRule states how one surface handles a protocol enum family: either
// every enum value matches patternTemplate (`%s` = enum value) in each listed
// file, or exemptReason documents why the surface intentionally delegates or
// omits it and guardSubstr proves that delegation still holds in code.
type surfaceRule struct {
	files           []string
	patternTemplate string // fmt.Sprintf(patternTemplate, regexp.QuoteMeta(val)); if empty, literal substring
	requiredSubstrs []string
	exemptReason    string
	guardFile       string
	guardSubstr     string
}

var cardKindSurfaceManifest = map[surfaceID]surfaceRule{
	surfaceIOS: {
		files:           []string{"ios/Sources/SheepdogKit/Cards.swift", "ios/Sources/Sheepdog/CardViews.swift"},
		patternTemplate: `\bcase\b[^\n]*\b%s\b`,
	},
	surfaceMacOS: {
		exemptReason: "menu-bar voice host delegates conversation cards to the mods/sheepdog panel",
		guardFile:    "ios/Sources/SheepdogMac/MacVoiceHost.swift",
		guardSubstr:  "case .transcript, .screen, .screenError, .card:",
	},
	surfacePanel: {
		files:           []string{"mods/sheepdog/model/cards.js", "mods/sheepdog/ui/cards.js"},
		patternTemplate: `case\s+"%s"\s*:`,
	},
	surfaceWeb: {
		files:           []string{"web/voice/cards.js"},
		patternTemplate: `case\s+'%s'\s*:`,
	},
}

var accountProblemSurfaceManifest = map[surfaceID]surfaceRule{
	surfaceIOS: {
		files:           []string{"ios/Sources/SheepdogKit/WSMessage.swift"},
		patternTemplate: `(?:\bcase\s+%s\b|"%s")`,
		requiredSubstrs: []string{
			"ios/Sources/Sheepdog/VoiceEngine.swift:message.displayErrorMessage",
			"ios/Sources/SheepdogKit/DaemonHealth.swift:\"brain.account_problem\"",
			"ios/Sources/SheepdogKit/DaemonHealth.swift:\"web.account_problem\"",
		},
	},
	surfaceMacOS: {
		files:           []string{"ios/Sources/SheepdogKit/WSMessage.swift"},
		patternTemplate: `(?:\bcase\s+%s\b|"%s")`,
		requiredSubstrs: []string{
			"ios/Sources/SheepdogMac/MacVoiceHost.swift:message.displayErrorMessage",
			"ios/Sources/SheepdogMac/NotificationService.swift:\"web.account_problem\"",
			"ios/Sources/SheepdogMac/NotificationService.swift:\"brain.account_problem\"",
		},
	},
	surfacePanel: {
		exemptReason: "panel does not open /ws; refreshes state on account_problem /events and renders voice.problem from /api/state",
		requiredSubstrs: []string{
			"mods/sheepdog/model/events.js:\"brain.account_problem\"",
			"mods/sheepdog/model/events.js:\"web.account_problem\"",
			"mods/sheepdog/model/status.js:voice.problem",
		},
	},
	surfaceWeb: {
		exemptReason: "browser /voice treats any truthy error.problem uniformly via showAccountProblem(m) displaying message and fix",
		requiredSubstrs: []string{
			"web/voice/app.js:showAccountProblem",
			"web/voice/app.js:m.problem",
			"web/voice/app.js:m.fix",
		},
	},
}

// sseProtocolFamilyRule records how each surface handles (or intentionally
// exempts) a cross-surface /events protocol event type.
type sseProtocolFamilyRule struct {
	handledIn    []string // files containing the event literal (or prefix)
	matchSubstr  string   // defaults to `"` + eventType + `"`
	exemptReason string
}

var sseCrossSurfaceManifest = map[string]map[surfaceID]sseProtocolFamilyRule{
	"pending.created": {
		surfaceIOS:   {handledIn: []string{"ios/Sources/SheepdogKit/ConversationHistory.swift", "ios/Sources/Sheepdog/SessionViewModel.swift"}},
		surfaceMacOS: {handledIn: []string{"ios/Sources/SheepdogMac/NotificationService.swift"}},
		surfacePanel: {handledIn: []string{"mods/sheepdog/model/events.js", "mods/sheepdog/model/fragments.js"}},
		surfaceWeb:   {exemptReason: "browser /voice speaks /ws only and does not subscribe to /events"},
	},
	"herdr.transition": {
		surfaceIOS:   {handledIn: []string{"ios/Sources/Sheepdog/SessionViewModel.swift"}, matchSubstr: `"herdr."`},
		surfaceMacOS: {handledIn: []string{"ios/Sources/SheepdogMac/NotificationService.swift"}},
		surfacePanel: {handledIn: []string{"mods/sheepdog/model/events.js"}},
		surfaceWeb:   {exemptReason: "browser /voice speaks /ws only and does not subscribe to /events"},
	},
	"conversation.reset": {
		surfaceIOS:   {handledIn: []string{"ios/Sources/SheepdogKit/ConversationHistory.swift", "ios/Sources/Sheepdog/SessionViewModel.swift"}},
		surfaceMacOS: {exemptReason: "menu-bar voice host has no conversation timeline; mods/sheepdog owns it"},
		surfacePanel: {handledIn: []string{"mods/sheepdog/model/events.js", "mods/sheepdog/model/fragments.js"}},
		surfaceWeb:   {exemptReason: "browser /voice speaks /ws only and does not subscribe to /events"},
	},
	"card.shown": {
		surfaceIOS:   {handledIn: []string{"ios/Sources/Sheepdog/SessionViewModel.swift"}},
		surfaceMacOS: {exemptReason: "menu-bar voice host delegates cards to mods/sheepdog"},
		surfacePanel: {handledIn: []string{"mods/sheepdog/model/events.js", "mods/sheepdog/model/fragments.js"}},
		surfaceWeb:   {exemptReason: "browser /voice receives cards directly over /ws card frames"},
	},
	"agent.attention": {
		surfaceIOS:   {handledIn: []string{"ios/Sources/SheepdogKit/AttentionActions.swift"}},
		surfaceMacOS: {exemptReason: "menu-bar voice host shows no per-agent attention groups; mods/sheepdog owns them"},
		surfacePanel: {handledIn: []string{"mods/sheepdog/model/events.js"}},
		surfaceWeb:   {exemptReason: "browser /voice has no agent roster"},
	},
	"space.muted": {
		surfaceIOS:   {handledIn: []string{"ios/Sources/SheepdogKit/AttentionActions.swift"}},
		surfaceMacOS: {exemptReason: "menu-bar voice host reads muted:true on pending.created/herdr.transition via AttentionEvents.isMuted"},
		surfacePanel: {handledIn: []string{"mods/sheepdog/model/events.js"}},
		surfaceWeb:   {exemptReason: "browser /voice has no space mute UI"},
	},
	"voice.closed": {
		surfaceIOS:   {handledIn: []string{"ios/Sources/SheepdogKit/ConversationHistory.swift"}},
		surfaceMacOS: {handledIn: []string{"ios/Sources/SheepdogMac/NotificationService.swift"}},
		surfacePanel: {handledIn: []string{"mods/sheepdog/model/events.js", "mods/sheepdog/model/fragments.js"}},
		surfaceWeb:   {exemptReason: "browser /voice receives voice closed status directly over /ws"},
	},
	"voice.connect_error": {
		surfaceIOS:   {handledIn: []string{"ios/Sources/SheepdogKit/DaemonHealth.swift", "ios/Sources/Sheepdog/SessionViewModel.swift"}},
		surfaceMacOS: {handledIn: []string{"ios/Sources/SheepdogMac/NotificationService.swift"}},
		surfacePanel: {handledIn: []string{"mods/sheepdog/model/events.js", "mods/sheepdog/model/fragments.js"}},
		surfaceWeb:   {exemptReason: "browser /voice receives voice errors directly over /ws"},
	},
	"web.account_problem": {
		surfaceIOS:   {handledIn: []string{"ios/Sources/SheepdogKit/DaemonHealth.swift", "ios/Sources/Sheepdog/SessionViewModel.swift"}},
		surfaceMacOS: {handledIn: []string{"ios/Sources/SheepdogMac/NotificationService.swift"}},
		surfacePanel: {handledIn: []string{"mods/sheepdog/model/events.js"}},
		surfaceWeb:   {exemptReason: "browser /voice receives account problems over /ws error.problem"},
	},
	"brain.account_problem": {
		surfaceIOS:   {handledIn: []string{"ios/Sources/SheepdogKit/DaemonHealth.swift", "ios/Sources/Sheepdog/SessionViewModel.swift"}},
		surfaceMacOS: {handledIn: []string{"ios/Sources/SheepdogMac/NotificationService.swift"}},
		surfacePanel: {handledIn: []string{"mods/sheepdog/model/events.js"}},
		surfaceWeb:   {exemptReason: "browser /voice receives account problems over /ws error.problem"},
	},
}

// legacyClientEventAllowlist documents /events names kept in clients solely so
// older daemons or historical event logs still render.
var legacyClientEventAllowlist = map[string]string{
	"supervisor.echo_dropped": "legacy daemon compatibility; superseded by supervisor.echo_possible in dual-brain ablation G6",
}

// clientSSEConsumerFiles are client source files that switch on or subscribe to
// /events type names.
var clientSSEConsumerFiles = []string{
	"mods/sheepdog/model/events.js",
	"mods/sheepdog/model/fragments.js",
	"ios/Sources/SheepdogKit/DaemonHealth.swift",
	"ios/Sources/SheepdogKit/ConversationHistory.swift",
	"ios/Sources/SheepdogKit/BrainActivity.swift",
	"ios/Sources/SheepdogKit/AttentionActions.swift",
	"ios/Sources/Sheepdog/SessionViewModel.swift",
	"ios/Sources/SheepdogMac/NotificationService.swift",
}

func TestSurfaceProtocolEnumParity(t *testing.T) {
	root := repoRoot(t)
	readFile := func(rel string) (string, error) {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			return "", err
		}
		return string(b), nil
	}

	cardKinds := parseGoStringConsts(t, filepath.Join(root, "internal/voice/wsproto/wsproto.go"), "CardKind")
	if len(cardKinds) == 0 {
		t.Fatal("found no CardKind* constants in internal/voice/wsproto/wsproto.go")
	}
	if errs := verifyEnumParity("CardKind", cardKinds, cardKindSurfaceManifest, readFile); len(errs) > 0 {
		for _, e := range errs {
			t.Error(e)
		}
	}

	problems := parseGoStringConsts(t, filepath.Join(root, "internal/brain/account.go"), "Problem")
	if len(problems) == 0 {
		t.Fatal("found no Problem* constants in internal/brain/account.go")
	}
	if errs := verifyEnumParity("AccountProblem", problems, accountProblemSurfaceManifest, readFile); len(errs) > 0 {
		for _, e := range errs {
			t.Error(e)
		}
	}
	// Verify ws-contract.md documents every CardKind and AccountProblem value.
	wsContract, err := readFile("docs/dev/ws-contract.md")
	if err != nil {
		t.Fatal(err)
	}
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

	// Verify /events SSE kinds, client consumers, and cross-surface manifest.
	sseKinds := parseSSEEventKinds(t, filepath.Join(root, "internal/web/sse.go"))
	emitted := collectDaemonEmittedEvents(t, root)
	if errs := verifySSEParity(sseKinds, emitted, clientSSEConsumerFiles, legacyClientEventAllowlist, sseCrossSurfaceManifest, readFile); len(errs) > 0 {
		for _, e := range errs {
			t.Error(e)
		}
	}
}

func TestParityCheckCatchesDrift(t *testing.T) {
	files := map[string]string{
		"ios/Sources/SheepdogKit/Cards.swift":        "case card, agent, excerpt, approval",
		"ios/Sources/Sheepdog/CardViews.swift":       "case .card: case .agent: case .excerpt: case .approval:",
		"ios/Sources/SheepdogMac/MacVoiceHost.swift": "case .transcript, .screen, .screenError, .card:",
		"mods/sheepdog/model/cards.js":               `case "card": case "agent": case "excerpt": case "approval":`,
		"mods/sheepdog/ui/cards.js":                  `case "card": case "agent": case "excerpt": case "approval":`,
		"web/voice/cards.js":                         `case 'card': case 'agent': case 'excerpt': case 'approval':`,
		"mods/sheepdog/model/events.js":              `"pending.created", "bogus.never_emitted"`,
	}
	fakeRead := func(rel string) (string, error) {
		s, ok := files[rel]
		if !ok {
			return "", fmt.Errorf("missing %s", rel)
		}
		return s, nil
	}

	// 1. Adding a new protocol enum value without updating surfaces must fail.
	errs := verifyEnumParity("CardKind", []string{"card", "agent", "excerpt", "approval", "diagram"}, cardKindSurfaceManifest, fakeRead)
	if len(errs) == 0 {
		t.Fatal("expected parity failure for unhandled CardKind \"diagram\"")
	}

	// 2. An exemption with an empty reason or missing guard must fail.
	badManifest := map[surfaceID]surfaceRule{
		surfaceIOS:   cardKindSurfaceManifest[surfaceIOS],
		surfaceMacOS: {exemptReason: ""},
		surfacePanel: cardKindSurfaceManifest[surfacePanel],
		surfaceWeb:   cardKindSurfaceManifest[surfaceWeb],
	}
	if errs := verifyEnumParity("CardKind", []string{"card"}, badManifest, fakeRead); len(errs) == 0 {
		t.Fatal("expected parity failure for undocumented surface exemption")
	}

	// 3. A daemon event whose prefix is missing from sse.go eventKinds must fail.
	sseErrs := verifySSEParity(
		map[string]bool{"pending": true},
		map[string]bool{"pending.created": true, "newfamily.started": true},
		nil,
		nil,
		nil,
		fakeRead,
	)
	if len(sseErrs) == 0 {
		t.Fatal("expected SSE parity failure when emitted prefix is missing from eventKinds")
	}

	// 4. A client referencing a non-existent SSE event type must fail.
	clientErrs := verifySSEParity(
		map[string]bool{"pending": true, "bogus": true},
		map[string]bool{"pending.created": true},
		[]string{"mods/sheepdog/model/events.js"},
		nil,
		nil,
		fakeRead,
	)
	if len(clientErrs) == 0 {
		t.Fatal("expected SSE parity failure for unknown client event \"bogus.never_emitted\"")
	}
}

func verifyEnumParity(
	enumName string,
	values []string,
	manifest map[surfaceID]surfaceRule,
	readFile func(string) (string, error),
) []string {
	var errs []string
	for _, surface := range allSurfaces {
		rule, ok := manifest[surface]
		if !ok {
			errs = append(errs, fmt.Sprintf("%s: surface %q missing from parity manifest", enumName, surface))
			continue
		}
		if rule.exemptReason != "" {
			if rule.guardFile != "" {
				src, err := readFile(rule.guardFile)
				if err != nil {
					errs = append(errs, fmt.Sprintf("%s [%s]: %v", enumName, surface, err))
				} else if !strings.Contains(src, rule.guardSubstr) {
					errs = append(errs, fmt.Sprintf("%s [%s]: exemption guard %q missing in %s", enumName, surface, rule.guardSubstr, rule.guardFile))
				}
			}
		} else {
			if len(rule.files) == 0 {
				errs = append(errs, fmt.Sprintf("%s [%s]: neither files nor exemptReason specified", enumName, surface))
				continue
			}
			for _, f := range rule.files {
				src, err := readFile(f)
				if err != nil {
					errs = append(errs, fmt.Sprintf("%s [%s]: %v", enumName, surface, err))
					continue
				}
				for _, val := range values {
					pat := regexp.QuoteMeta(val)
					if rule.patternTemplate != "" {
						pat = strings.ReplaceAll(rule.patternTemplate, "%s", regexp.QuoteMeta(val))
					}
					re := regexp.MustCompile(pat)
					if !re.MatchString(src) {
						errs = append(errs, fmt.Sprintf("%s [%s]: %s does not handle %q (pattern %s)", enumName, surface, f, val, pat))
					}
				}
			}
		}
		for _, req := range rule.requiredSubstrs {
			f, needle, ok := strings.Cut(req, ":")
			if !ok {
				errs = append(errs, fmt.Sprintf("%s [%s]: malformed requiredSubstr %q", enumName, surface, req))
				continue
			}
			src, err := readFile(f)
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s [%s]: %v", enumName, surface, err))
				continue
			}
			if !strings.Contains(src, needle) {
				errs = append(errs, fmt.Sprintf("%s [%s]: %s missing %q", enumName, surface, f, needle))
			}
		}
	}
	return errs
}

var quotedEventLiteralRe = regexp.MustCompile(`["']([a-z_]+\.[a-z_.]*)["']`)

func verifySSEParity(
	sseKinds map[string]bool,
	emitted map[string]bool,
	consumerFiles []string,
	allowlist map[string]string,
	crossManifest map[string]map[surfaceID]sseProtocolFamilyRule,
	readFile func(string) (string, error),
) []string {
	var errs []string

	// 1. Every emitted Go event's first dotted segment must be in sse.go eventKinds.
	var emittedSorted []string
	for ev := range emitted {
		emittedSorted = append(emittedSorted, ev)
	}
	sort.Strings(emittedSorted)
	for _, ev := range emittedSorted {
		kind, _, _ := strings.Cut(ev, ".")
		if !sseKinds[kind] {
			errs = append(errs, fmt.Sprintf("daemon emits %q, whose prefix %q is missing from internal/web/sse.go eventKinds", ev, kind))
		}
	}

	// 2. Every dotted event literal in client consumer files whose prefix is an
	// SSE kind must exist in emitted Go events (or match as a prefix when
	// ending in "." or "_", e.g. "herdr." or "voice.push_"), or be allowlisted.
	for _, f := range consumerFiles {
		src, err := readFile(f)
		if err != nil {
			errs = append(errs, fmt.Sprintf("SSE consumer %s: %v", f, err))
			continue
		}
		src = stripLineComments(src)
		for _, m := range quotedEventLiteralRe.FindAllStringSubmatch(src, -1) {
			lit := m[1]
			kind, rest, _ := strings.Cut(lit, ".")
			if !sseKinds[kind] {
				continue
			}
			if rest == "" || strings.HasSuffix(lit, ".") || strings.HasSuffix(lit, "_") {
				matched := false
				for _, ev := range emittedSorted {
					if strings.HasPrefix(ev, lit) {
						matched = true
						break
					}
				}
				if !matched {
					errs = append(errs, fmt.Sprintf("%s filters on prefix %q which matches no daemon event", f, lit))
				}
				continue
			}
			if emitted[lit] {
				continue
			}
			if reason, ok := allowlist[lit]; ok && reason != "" {
				continue
			}
			errs = append(errs, fmt.Sprintf("%s references /events type %q which the daemon never emits", f, lit))
		}
	}

	// 3. Cross-surface manifest for key /events families.
	var families []string
	for fam := range crossManifest {
		families = append(families, fam)
	}
	sort.Strings(families)
	for _, fam := range families {
		if !emitted[fam] {
			errs = append(errs, fmt.Sprintf("cross-surface manifest lists %q, which the daemon does not emit", fam))
			continue
		}
		perSurface := crossManifest[fam]
		for _, surface := range allSurfaces {
			rule, ok := perSurface[surface]
			if !ok {
				errs = append(errs, fmt.Sprintf("/events %q: surface %q missing from cross-surface manifest", fam, surface))
				continue
			}
			if rule.exemptReason != "" {
				continue
			}
			if len(rule.handledIn) == 0 {
				errs = append(errs, fmt.Sprintf("/events %q [%s]: neither handledIn nor exemptReason set", fam, surface))
				continue
			}
			needle := rule.matchSubstr
			if needle == "" {
				needle = `"` + fam + `"`
			}
			for _, f := range rule.handledIn {
				src, err := readFile(f)
				if err != nil {
					errs = append(errs, fmt.Sprintf("/events %q [%s]: %v", fam, surface, err))
					continue
				}
				if !strings.Contains(src, needle) {
					errs = append(errs, fmt.Sprintf("/events %q [%s]: %s missing %s", fam, surface, f, needle))
				}
			}
		}
	}
	return errs
}

func stripLineComments(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if idx := strings.Index(line, "//"); idx >= 0 {
			// Only strip when // is outside quotes on simple comment lines.
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "///") {
				continue
			}
			_ = idx
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
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
