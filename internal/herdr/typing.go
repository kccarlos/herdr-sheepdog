package herdr

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

var inputMarker = regexp.MustCompile(`^\s*[❯›](?:[\s\p{Zs}]|$)`)
var inputRule = regexp.MustCompile(`^\s*[─━]{8,}`)

// TypedInput reports whether the bottom-most input row contains non-dim text,
// and whether its styling was available to make that decision.
func HasTypedInput(screen string) bool { typed, _ := TypedInput(screen); return typed }
func TypedInput(screen string) (bool, bool) {
	type cell struct {
		r   rune
		dim bool
	}
	var lines [][]cell
	var line []cell
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
			line = append(line, cell{rs[i], dim})
		}
	}
	lines = append(lines, line)
	styledLines = append(styledLines, styled)
	plain := func(l []cell) string {
		var b strings.Builder
		for _, c := range l {
			b.WriteRune(c.r)
		}
		return b.String()
	}
	// Claude encloses its input in horizontal rules. Codex uses an
	// unboxed prompt immediately above its footer. Never search scrollback.
	end := len(lines)
	for end > 0 && strings.TrimSpace(plain(lines[end-1])) == "" {
		end--
	}
	bottomRule := -1
	for i := end - 1; i >= 0 && i >= end-3; i-- {
		if inputRule.MatchString(plain(lines[i])) {
			bottomRule = i
			break
		}
	}
	start := 0
	boxed := false
	if bottomRule >= 0 {
		end = bottomRule
		for i := end - 1; i >= 0; i-- {
			if inputRule.MatchString(plain(lines[i])) {
				start, boxed = i+1, true
				break
			}
		}
	}
	for i := end - 1; i >= start; i-- {
		s := plain(lines[i])
		loc := inputMarker.FindStringIndex(s)
		if loc == nil {
			continue
		}
		if !boxed {
			// An unboxed Codex input can have indented continuation rows,
			// then a blank line and a single indented status footer.
			footer := false
			for j := i + 1; j < end; j++ {
				next := plain(lines[j])
				if strings.TrimSpace(next) == "" {
					footer = true
					continue
				}
				if (next[0] != ' ' && next[0] != '\t') || (footer && j != end-1) {
					return false, false
				}
			}
		}
		if boxed {
			for j := i + 1; j < end; j++ {
				next := plain(lines[j])
				if strings.TrimSpace(next) != "" && next[0] != ' ' && next[0] != '\t' {
					return false, false
				}
			}
		}
		if !styledLines[i] {
			return false, false
		}
		prefix := len([]rune(s[:loc[1]]))
		for j := i; j < end; j++ {
			l := lines[j]
			if j > i && !boxed && strings.TrimSpace(plain(l)) == "" {
				break
			}
			if j == i {
				l = l[prefix:]
			}
			for _, c := range l {
				if !c.dim && !unicode.IsSpace(c.r) {
					return true, true
				}
			}
		}
		return false, true
	}
	return false, false
}
