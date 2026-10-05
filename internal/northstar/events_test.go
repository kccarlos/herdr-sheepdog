package northstar

import (
	"strings"
	"testing"
)

const benchEvents = `{"at_ms":4,"type":"web.inject","pane":"w1:p1","payload":{"alias":"bench-codex","agent":"codex"}}
{"at_ms":5,"type":"herdr.transition","pane":"w1:p1","payload":{"agent":"codex","from":"working","to":"blocked"}}
{"at_ms":1000,"type":"brain.tool_call","payload":{"id":"c1","name":"run_cli","args":{"agent":"bench-codex"}}}
{"at_ms":9000,"type":"brain.tool_timeout","payload":{"id":"c1","name":"run_cli"}}
{"at_ms":9000,"type":"brain.tool_result","payload":{"id":"c1","name":"run_cli","result":""}}
{"at_ms":9500,"type":"brain.tool_call","payload":{"id":"c2","name":"investigate","args":{"agent":"bench-codex"}}}
{"at_ms":9501,"type":"brain.tool_result","payload":{"id":"c2","name":"investigate","result":"{\"job\":\"inv-1\"}"}}
{"at_ms":12000,"type":"herdr.send","pane":"w1:p1","payload":{"kind":"keys","keys":["esc"]}}
{"at_ms":12001,"type":"pending.confirmed","pane":"w1:p1","payload":{"answer":"no","keys":["esc"]}}
{"at_ms":13000,"type":"voice.tool_result","payload":{"spoken":"I declined it."}}
{"at_ms":13100,"type":"voice.push","payload":{"text":"names line","urgency":"silent","from":"daemon"}}
{"at_ms":13500,"type":"voice.transcript.out","payload":{"text":"I declined "}}
{"at_ms":13900,"type":"voice.transcript.out","payload":{"text":"it."}}
{"at_ms":20000,"type":"voice.transcript.out","payload":{"text":"Anything else?"}}
{"at_ms":30000,"type":"investigation.done","payload":{"agent":"bench-codex"}}
{"at_ms":31000,"type":"supervisor.spawn","payload":{"kind":"claude","dir":"/x/bench"}}
{"at_ms":32000,"type":"supervisor.spawn_error","payload":{"error":"boom"}}
{"at_ms":33000,"type":"supervisor.close","payload":{"agent":"docs-claude"}}
{"at_ms":34000,"type":"turn.latency","payload":{"total_ms":4200}}
not json
`

func TestReadFacts(t *testing.T) {
	evs, err := ParseEvents(strings.NewReader(benchEvents))
	if err != nil {
		t.Fatal(err)
	}
	f := ReadFacts(evs)
	byKind := map[ActionKind][]Action{}
	for _, a := range f.Actions {
		byKind[a.Kind] = append(byKind[a.Kind], a)
	}
	if c := byKind[ActCheck]; len(c) != 2 || c[0].Outcome != OutcomeTimeout || c[0].Tool != "run_cli" || c[1].Outcome != OutcomeOK {
		t.Errorf("checks = %+v", c)
	}
	if c := byKind[ActCheckStarted]; len(c) != 1 {
		t.Errorf("an investigation job only starts a check: %+v", c)
	}
	if s := byKind[ActSpawn]; len(s) != 1 || s[0].Outcome != OutcomeError {
		t.Errorf("spawn = %+v", s)
	}
	if c := byKind[ActClose]; len(c) != 1 || c[0].Agent != "docs-claude" {
		t.Errorf("close = %+v", c)
	}
	if len(f.Sends) != 1 || f.Sends[0].Agent != "bench-codex" || f.Sends[0].Kind != ActKeys {
		t.Errorf("sends = %+v", f.Sends)
	}
	AttachAnswers(f.Sends, f.Answers)
	if f.Sends[0].Answer != "no" {
		t.Errorf("answer = %q", f.Sends[0].Answer)
	}
	if len(f.Orchestrator) != 1 || f.Orchestrator[0].Text != "I declined it." {
		t.Errorf("orchestrator = %+v (silent daemon pushes are left out)", f.Orchestrator)
	}
	if len(f.Voice) != 2 || f.Voice[0].Text != "I declined it." {
		t.Errorf("voice lines = %+v", f.Voice)
	}
	if len(f.Transitions) != 1 || f.Transitions[0].Kind != NewsBlocked || f.Transitions[0].Agent != "bench-codex" {
		t.Errorf("transitions = %+v", f.Transitions)
	}
	kinds := map[NewsKind]int{}
	for _, n := range f.News {
		kinds[n.Kind]++
	}
	if kinds[NewsResult] != 1 || kinds[NewsSpawnFailed] != 1 {
		t.Errorf("news = %+v", f.News)
	}
	if len(f.LatencyMS) != 1 || f.LatencyMS[0] != 4200 {
		t.Errorf("latency = %v", f.LatencyMS)
	}
}

func TestParseEventsWallClockAndAlign(t *testing.T) {
	log := `{"ts":"2026-10-05T09:00:00Z","type":"daemon.start"}
{"ts":"2026-10-05T09:00:10Z","type":"supervisor.relay","payload":{"heard":"hi"}}
{"ts":"2026-10-05T09:00:15Z","type":"herdr.send","pane_id":"w1:p1","payload":{"kind":"prompt","text":"x"}}
`
	evs, _ := ParseEvents(strings.NewReader(log))
	if evs[2].AtMS != 15000 || evs[2].Pane != "w1:p1" {
		t.Fatalf("parsed = %+v", evs[2])
	}
	Align(evs, 6000, -1)
	if evs[2].AtMS != 6000 || evs[1].AtMS != 1000 {
		t.Fatalf("aligned on the send: %+v", evs)
	}
	Align(evs, -1, 500)
	if evs[1].AtMS != 500 {
		t.Fatalf("aligned on the relay: %+v", evs[1])
	}
}

func TestNewsFromTimeline(t *testing.T) {
	cases := []struct {
		text string
		kind NewsKind
		ok   bool
	}{
		{"bench-claude is now done; its screen ends: …", NewsFinished, true},
		{"bench-codex is now blocked; its screen ends: …", NewsBlocked, true},
		{"bench-codex is now working; …", "", false},
		{"bench-claude: working → idle (herdr)", NewsFinished, true},
		{"bench-claude: new → idle (herdr)", "", false},
		{"bench-claude: new → blocked (herdr)", NewsBlocked, true},
	}
	for _, c := range cases {
		n, ok := NewsFromTimeline(1, "", c.text)
		if ok != c.ok || (ok && n.Kind != c.kind) {
			t.Errorf("%q: %+v %v", c.text, n, ok)
		}
	}
	if n, _ := NewsFromTimeline(1, "", "new agent in pane w1:p2: working → idle (herdr)"); n.Agent != "" {
		t.Errorf("an unnamed new agent: %+v", n)
	}
}

func TestRunFromLogAndSessionBlock(t *testing.T) {
	log := `{"ts":"2026-10-05T09:00:00Z","type":"pending.created","pane_id":"w1:p1","payload":{"agent":"auth-codex"}}
{"ts":"2026-10-05T09:00:01Z","type":"supervisor.relay","payload":{"heard":"yes, approve it"}}
{"ts":"2026-10-05T09:00:03Z","type":"herdr.send","pane_id":"w1:p1","payload":{"kind":"keys","keys":["y"]}}
{"ts":"2026-10-05T09:00:04Z","type":"voice.tool_result","payload":{"spoken":"I've approved it."}}
{"ts":"2026-10-05T09:00:05Z","type":"voice.transcript.out","payload":{"text":"I've approved it."}}
{"ts":"2026-10-05T09:00:20Z","type":"herdr.transition","pane_id":"w1:p1","payload":{"from":"working","to":"idle"}}
{"ts":"2026-10-05T12:00:00Z","type":"herdr.transition","pane_id":"w1:p1","payload":{"from":"working","to":"done"}}
`
	evs, _ := ParseEvents(strings.NewReader(log))
	r := RunFromLog("s1", evs)
	if r.Heard != SideVoice || len(r.User) != 1 || len(r.Actions) != 1 || r.Actions[0].Agent != "auth-codex" {
		t.Fatalf("run = %+v", r)
	}
	if len(r.News) != 1 || r.NewsOutside != 1 {
		t.Fatalf("news inside %d, outside %d", len(r.News), r.NewsOutside)
	}
	b := SummarizeSession(r)
	if b.Done.K != 1 || b.Done.N != 1 || b.Safe.K != 1 || b.MissedNews.K != 1 {
		t.Fatalf("session block = %+v", b)
	}
	var sb strings.Builder
	b.WriteMarkdown(&sb)
	if !strings.Contains(sb.String(), "no judge on real sessions") {
		t.Errorf("markdown:\n%s", sb.String())
	}
}
