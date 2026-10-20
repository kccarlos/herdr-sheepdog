package termproto

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestContractExamplesRoundTrip keeps docs/dev/term-contract.md and this
// package from drifting: every line of every ```json block parses into
// Message with no unknown field, validates, and re-encodes to the same
// JSON (ignoring key order).
func TestContractExamplesRoundTrip(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "dev", "term-contract.md"))
	if err != nil {
		t.Fatal(err)
	}
	in, n := false, 0
	types := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		switch {
		case !in && text == "```json":
			in = true
			continue
		case in && text == "```":
			in = false
			continue
		case !in || text == "":
			continue
		}
		n++
		dec := json.NewDecoder(strings.NewReader(text))
		dec.DisallowUnknownFields()
		var m Message
		if err := dec.Decode(&m); err != nil {
			t.Errorf("line %d: %v: %s", line, err, text)
			continue
		}
		if err := m.Validate(); err != nil {
			t.Errorf("line %d: invalid: %v: %s", line, err, text)
		}
		types[m.Type] = true
		again, _ := json.Marshal(m)
		var x, y any
		_ = json.Unmarshal([]byte(text), &x)
		_ = json.Unmarshal(again, &y)
		if !reflect.DeepEqual(x, y) {
			t.Errorf("line %d: round trip\n doc  %s\n code %s", line, text, again)
		}
	}
	if in {
		t.Fatal("unterminated ```json block")
	}
	for _, typ := range []string{TypeHello, TypeOpen, TypeResize, TypeScroll, TypeMouse, TypeClose, TypeReady, TypeOpened, TypeClosed, TypeError} {
		if !types[typ] {
			t.Errorf("the contract has no example of %s", typ)
		}
	}
	if n < 10 {
		t.Fatalf("only %d examples", n)
	}
}
