package herdr

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

var inputMarker = regexp.MustCompile(`^\s*[❯›]\s`)
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
			if rs[j] == 'm' {
				styled = true
				parts := strings.Split(string(rs[i+2:j]), ";")
				for k := 0; k < len(parts); k++ {
					n, _ := strconv.Atoi(parts[k])
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
	for i := len(lines) - 1; i >= 0; i-- {
		s := plain(lines[i])
		loc := inputMarker.FindStringIndex(s)
		if loc == nil {
			continue
		}
		if !styledLines[i] {
			return false, false
		}
		prefix := len([]rune(s[:loc[1]]))
		for j := i; j < len(lines); j++ {
			l := lines[j]
			if j > i && (strings.TrimSpace(plain(l)) == "" || inputRule.MatchString(plain(l))) {
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
