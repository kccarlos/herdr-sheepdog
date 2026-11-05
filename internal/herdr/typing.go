package herdr

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

var inputMarker = regexp.MustCompile(`^\s*[❯›](?:[\s\p{Zs}]|$)`)
var inputRule = regexp.MustCompile(`^\s*[─━]{8,}`)

type screenCell struct {
	r   rune
	dim bool
}

type inputRegion struct {
	lines      [][]screenCell
	styled     []bool
	row        int
	end        int
	prefix     int
	boxed      bool
	bottomRule int
	footer     bool
}

// TypedInput reports whether the bottom-most input row contains non-dim text,
// and whether its styling was available to make that decision.
func HasTypedInput(screen string) bool { typed, _ := TypedInput(screen); return typed }

func TypedInput(screen string) (bool, bool) {
	reg, ok := findInputRegion(screen)
	if !ok || !reg.styled[reg.row] {
		return false, false
	}
	for j := reg.row; j < reg.end; j++ {
		l := reg.lines[j]
		if j > reg.row && !reg.boxed && strings.TrimSpace(plainCells(l)) == "" {
			break
		}
		if j == reg.row {
			l = l[reg.prefix:]
		}
		for _, c := range l {
			if !c.dim && !unicode.IsSpace(c.r) {
				return true, true
			}
		}
	}
	return false, true
}

// TypedInputPlain reports whether a plain-text screen's live input box holds
// unsent desktop text, and whether the screen clearly showed the input state.
// Backends without colour reads (herdr-core) use this: empty prompts and known
// idle placeholders allow sending, framed non-placeholder text blocks sending,
// and anything unclear returns (false, false) so the caller fails open and logs.
func TypedInputPlain(screen string) (bool, bool) {
	reg, ok := findInputRegion(screen)
	if !ok {
		return false, false
	}
	var parts []string
	for j := reg.row; j < reg.end; j++ {
		l := reg.lines[j]
		if j > reg.row && !reg.boxed && strings.TrimSpace(plainCells(l)) == "" {
			break
		}
		if j == reg.row {
			l = l[reg.prefix:]
		}
		if s := strings.TrimFunc(plainCells(l), unicode.IsSpace); s != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return false, true
	}
	if isInputPlaceholder(strings.Join(parts, " ")) {
		return false, true
	}
	if !reg.boxed && reg.bottomRule < 0 && !reg.footer {
		return false, false
	}
	return true, true
}

func isInputPlaceholder(s string) bool {
	switch {
	case strings.HasPrefix(s, `Try "`) && (strings.HasSuffix(s, `"`) || strings.HasSuffix(s, "…")):
		return true
	case strings.HasPrefix(s, "Press up to "):
		return true
	case strings.HasPrefix(s, "Press Enter to edit the selected message,"):
		return true
	case strings.HasPrefix(s, "Message @") && strings.HasSuffix(s, "…"):
		return true
	case s == "Ask Codex to do anything", s == "Ask a follow-up question":
		return true
	default:
		return false
	}
}

func plainCells(l []screenCell) string {
	var b strings.Builder
	for _, c := range l {
		b.WriteRune(c.r)
	}
	return b.String()
}

func findInputRegion(screen string) (inputRegion, bool) {
	var lines [][]screenCell
	var line []screenCell
	var styledLines []bool
	styled := false
	dim := false
	rs := []rune(screen)
	for i := 0; i < len(rs); i++ {
		if rs[i] == '\x1b' && i+1 < len(rs) && rs[i+1] == '[' {
			j := i + 2
			for j < len(rs) && (rs[j] < '@' || rs[j] > '~') {
				j++
			}
			if j == len(rs) {
				break
			}
			if rs[j] == 'm' && (i+2 == j || rs[i+2] < '<' || rs[i+2] > '?') {
				styled = true
				parts := strings.Split(string(rs[i+2:j]), ";")
				for k := 0; k < len(parts); k++ {
					part := strings.SplitN(parts[k], ":", 2)[0]
					n, err := strconv.Atoi(part)
					if err != nil && part != "" {
						continue
					}
					switch n {
					case 0, 22:
						dim = false
					case 2:
						dim = true
					case 38, 48, 58:
						if k+1 < len(parts) {
							switch parts[k+1] {
							case "2":
								k += 4
							case "5":
								k += 2
							}
						}
					}
				}
			}
			i = j
			continue
		}
		if rs[i] == '\n' {
			lines = append(lines, line)
			styledLines = append(styledLines, styled)
			styled = false
			line = nil
		} else if rs[i] >= ' ' {
			line = append(line, screenCell{rs[i], dim})
		}
	}
	lines = append(lines, line)
	styledLines = append(styledLines, styled)

	// Claude encloses its input in horizontal rules. Codex uses an
	// unboxed prompt immediately above its footer. Never search scrollback.
	end := len(lines)
	for end > 0 && strings.TrimSpace(plainCells(lines[end-1])) == "" {
		end--
	}
	bottomRule := -1
	for i := end - 1; i >= 0 && i >= end-3; i-- {
		if inputRule.MatchString(plainCells(lines[i])) {
			bottomRule = i
			break
		}
	}
	start := 0
	boxed := false
	if bottomRule >= 0 {
		end = bottomRule
		for i := end - 1; i >= 0; i-- {
			if inputRule.MatchString(plainCells(lines[i])) {
				start, boxed = i+1, true
				break
			}
		}
	}
	for i := end - 1; i >= start; i-- {
		s := plainCells(lines[i])
		loc := inputMarker.FindStringIndex(s)
		if loc == nil {
			continue
		}
		footer := false
		if !boxed {
			// An unboxed Codex input can have indented continuation rows,
			// then a blank line and a single indented status footer.
			for j := i + 1; j < end; j++ {
				next := plainCells(lines[j])
				if strings.TrimSpace(next) == "" {
					footer = true
					continue
				}
				if (next[0] != ' ' && next[0] != '\t') || (footer && j != end-1) {
					return inputRegion{}, false
				}
			}
		}
		if boxed {
			for j := i + 1; j < end; j++ {
				next := plainCells(lines[j])
				if strings.TrimSpace(next) != "" && next[0] != ' ' && next[0] != '\t' {
					return inputRegion{}, false
				}
			}
		}
		return inputRegion{
			lines:      lines,
			styled:     styledLines,
			row:        i,
			end:        end,
			prefix:     len([]rune(s[:loc[1]])),
			boxed:      boxed,
			bottomRule: bottomRule,
			footer:     footer,
		}, true
	}
	return inputRegion{}, false
}
