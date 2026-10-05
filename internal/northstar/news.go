package northstar

import (
	"regexp"
	"strings"
)

// News outcomes.
const (
	NewsConveyed = "conveyed"
	NewsMissed   = "missed"
	// NewsUngraded: the call ended before the window closed and nothing
	// had conveyed it yet.
	NewsUngraded = "ungraded"
)

// NewsItem is one piece of news and whether the user heard it.
type NewsItem struct {
	News
	Outcome string `json:"outcome"`
	// By is the line that conveyed it.
	By string `json:"by,omitempty"`
}

// MissedNews is a run's missed-news check.
type MissedNews struct {
	Items []NewsItem `json:"items,omitempty"`
}

// Count returns how many items had outcome o.
func (m MissedNews) Count(o string) int {
	n := 0
	for _, it := range m.Items {
		if it.Outcome == o {
			n++
		}
	}
	return n
}

// newsWords are the words that carry a kind of news, in English and
// Chinese (measurement only).
var newsWords = map[NewsKind]*regexp.Regexp{
	NewsFinished:    regexp.MustCompile(`(?i)\b(finish|finished|finishes|done|complete|completed|wrapped up|ready|result|results|passed|passing|fixed|added|wrote|written|created|updated|committed|idle|stopped)\b|完成|做完|好了|完了|完毕|完畢|分析完|查完|跑完|弄完|改完|写好|寫好|结束|結束|搞定|写完|寫完|改好|修好|通过|通過|加好|空闲|空閒|停下`),
	NewsBlocked:     regexp.MustCompile(`(?i)\b(wait|waiting|waits|approval|approve|permission|asks|asking|asked|proceed|question|decide|decision|needs you|need you|blocked|prompt|trust|allow|confirm|options?|yes or no|go ahead)\b|等|批准|同意|允许|允許|确认|確認|要不要|问|問|需要你|权限|權限|是否|选择|選擇|继续吗|繼續嗎`),
	NewsResult:      regexp.MustCompile(`(?i)\b(found|finds|result|results|finding|findings|answer|says|said|investigation|looked into|checked|turns out|because|couldn't|could not|failed)\b|发现|發現|结果|結果|查到|原因|因为|因為|看了|查了|没能|沒能|失败|失敗`),
	NewsSpawnFailed: regexp.MustCompile(`(?i)\b(couldn't|could not|can't|cannot|failed|unable|error|didn't start|did not start|not ready|refused)\b|没能|沒能|无法|無法|失败|失敗|不能|没有启动|沒有啟動|出错|出錯`),
}

// CheckMissedNews runs the missed-news check on the side the user hears:
// each piece of news is conveyed when a line after it, before the user's
// next line or within MentionWindowMS (whichever is later), carries a word
// of its kind and, when more than one agent is known and the news names
// one, a name of that agent. When the call ended before that window
// closed with nothing said, the news is ungraded rather than missed.
func CheckMissedNews(r Run) MissedNews {
	var out MissedNews
	var lines []Speech
	for _, s := range r.Speech {
		if s.Side == r.Heard {
			lines = append(lines, s)
		}
	}
	for _, n := range dedupeNews(r.News) {
		deadline := n.AtMS + MentionWindowMS
		next := nextUser(r, n.AtMS)
		if next > deadline {
			deadline = next
		}
		res := NewsItem{News: n, Outcome: NewsMissed}
		for _, s := range lines {
			if s.AtMS < n.AtMS || s.AtMS > deadline {
				continue
			}
			if conveys(r, n, s.Text) {
				res.Outcome, res.By = NewsConveyed, s.Text
				break
			}
		}
		if res.Outcome == NewsMissed && next < 0 && r.EndMS < deadline {
			res.Outcome = NewsUngraded
		}
		out.Items = append(out.Items, res)
	}
	return out
}

// dedupeNews drops a repeat of the same news about the same agent within
// MentionWindowMS (a flapping status).
func dedupeNews(ns []News) []News {
	var out []News
	last := map[string]int64{}
	for _, n := range ns {
		k := string(n.Kind) + "\x00" + n.Agent
		if t, ok := last[k]; ok && n.AtMS-t < MentionWindowMS {
			continue
		}
		last[k] = n.AtMS
		out = append(out, n)
	}
	return out
}

func conveys(r Run, n News, text string) bool {
	ok := newsWords[n.Kind].MatchString(text)
	if n.Kind == NewsFinished {
		// A finish is conveyed by what the worker found or did, too.
		ok = ok || newsWords[NewsResult].MatchString(text)
	}
	if !ok {
		return false
	}
	if n.Agent == "" || len(r.Agents) <= 1 {
		return true
	}
	return mentionsAgent(r.Agents, n.Agent, text)
}

// mentionsAgent reports whether text names agent: its alias, the alias
// without a bench- prefix, its spoken name, or a part of its alias (three
// letters or more) or its kind that no other known agent shares ("auth"
// for auth-codex next to docs-codex, but not "codex").
func mentionsAgent(agents []Agent, alias, text string) bool {
	if strings.HasPrefix(alias, "new agent in pane") {
		// An agent started during the call, not named yet in the record.
		return true
	}
	low := strings.ToLower(text)
	parts := func(a Agent) []string {
		var out []string
		for _, n := range []string{a.Alias, a.Kind} {
			for _, p := range strings.FieldsFunc(strings.ToLower(n), func(r rune) bool { return r == '-' || r == '_' || r == ' ' }) {
				if len(p) >= 3 && p != "bench" {
					out = append(out, p)
				}
			}
		}
		return out
	}
	target := Agent{Alias: alias}
	shared := map[string]int{}
	for _, a := range agents {
		if a.Alias == alias {
			target = a
		}
		seen := map[string]bool{}
		for _, p := range parts(a) {
			if !seen[p] {
				seen[p] = true
				shared[p]++
			}
		}
	}
	names := []string{target.Alias, strings.TrimPrefix(target.Alias, "bench-"), target.Spoken}
	for _, p := range parts(target) {
		if shared[p] <= 1 {
			names = append(names, p)
		}
	}
	for _, n := range names {
		if n = strings.ToLower(strings.TrimSpace(n)); n != "" && strings.Contains(low, n) {
			return true
		}
	}
	return false
}
