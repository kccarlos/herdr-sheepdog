package config

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// A config error names the file, the line and the key, and says what is
// wrong, so a user who mistyped one line can fix it without reading the
// source:
//
//	config: /home/me/.config/sheepdog/config.toml line 3, key web.port: wrong type: want an integer, got a string
//
// parseError rewrites a TOML decode error that way; locate adds the file
// and line to a validation error ("config: supervisor.chattiness=…").

// typeNames turns toml's Go type names into the words a user knows.
var typeNames = map[string]string{
	"integer": "an integer", "string": "a string (in quotes)", "bool": "true or false",
	"boolean": "true or false", "float": "a number", "array": "a list [ … ]",
	"slice": "a list [ … ]", "table": "a section", "map": "a section", "struct": "a section",
	"time.Time": "a date", "datetime": "a date",
}

var reDecodeErr = regexp.MustCompile(`^toml: line (\d+)(?: \(last key "([^"]*)"\))?: (.*)$`)

var reIncompatible = regexp.MustCompile(`incompatible types: TOML value has type ([\w.]+); destination has type ([\w.]+)`)

func typeWord(t string) string {
	if w, ok := typeNames[t]; ok {
		return w
	}
	return t
}

// parseError rewrites a toml.Decode error so it names path, the line
// and the key.
func parseError(path string, err error) error {
	var pe toml.ParseError
	if pp := (*toml.ParseError)(nil); errors.As(err, &pp) && pp != nil {
		pe = *pp
	} else if !errors.As(err, &pe) {
		// A type mismatch is a plain error:
		// toml: line 2 (last key "web.port"): incompatible types: …
		m := reDecodeErr.FindStringSubmatch(err.Error())
		if m == nil {
			return fmt.Errorf("config: %s: %w", path, err)
		}
		pe.Position.Line, _ = strconv.Atoi(m[1])
		pe.LastKey, pe.Message = m[2], m[3]
	}
	msg := pe.Message
	if m := reIncompatible.FindStringSubmatch(msg); m != nil {
		msg = fmt.Sprintf("wrong type: want %s, got %s", typeWord(m[2]), typeWord(m[1]))
	}
	line := pe.Position.Line
	where := fmt.Sprintf("%s line %d", path, line)
	if pe.LastKey != "" {
		where += ", key " + pe.LastKey
	}
	return fmt.Errorf("config: %s: %s", where, msg)
}

// reKeyErr picks the key out of a validation error "config: a.b=…".
var reKeyErr = regexp.MustCompile(`^config: ([a-z0-9_]+(?:\.[a-z0-9_]+)*)(?:\[\d+\])?[=:]`)

// locate adds "<path> line N" to a validation error about a key the file
// sets. An error about a key the file does not set (a default or an
// environment override) is returned unchanged.
func locate(path string, data []byte, err error) error {
	if err == nil || data == nil {
		return err
	}
	m := reKeyErr.FindStringSubmatch(err.Error())
	if m == nil {
		return err
	}
	key := m[1]
	n := keyLine(data, key)
	if n == 0 {
		return err
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(err.Error(), "config: "), m[0][len("config: "):len(m[0])-1])
	if r, ok := strings.CutPrefix(rest, "="); ok {
		rest = r
		if val, why, ok := strings.Cut(rest, ": "); ok {
			rest = val + " is not valid (" + why + ")"
		}
	} else {
		rest = strings.TrimSpace(strings.TrimPrefix(rest, ":"))
	}
	return fmt.Errorf("config: %s line %d, key %s: %s", path, n, key, rest)
}

// keyLine is the 1-based line of the file that sets the dotted key
// ("supervisor.chattiness": `chattiness = …` under [supervisor]), or 0.
// It understands [section] and [[array]] headers and dotted keys; it is
// a locator for messages, not a parser.
func keyLine(data []byte, key string) int {
	section := ""
	for i, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			h := strings.Trim(line, "[] \t")
			if j := strings.Index(h, "#"); j >= 0 {
				h = strings.Trim(h[:j], "[] \t")
			}
			section = strings.ReplaceAll(h, " ", "")
			continue
		}
		k, _, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.ReplaceAll(strings.Trim(strings.TrimSpace(k), `"`), " ", "")
		full := k
		if section != "" {
			full = section + "." + k
		}
		if full == key {
			return i + 1
		}
	}
	return 0
}
