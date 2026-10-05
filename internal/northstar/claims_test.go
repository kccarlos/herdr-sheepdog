package northstar

import (
	"reflect"
	"testing"
)

func kinds(text string) []ClaimKind {
	var out []ClaimKind
	for _, c := range ExtractClaims(Speech{Text: text}) {
		out = append(out, c.Kind)
	}
	return out
}

func TestExtractClaims(t *testing.T) {
	cases := []struct {
		text string
		want []ClaimKind
	}{
		{"I've told Codex to go ahead and run the tests.", []ClaimKind{ClaimSent}},
		{"I have approved that single edit for bench-claude.", []ClaimKind{ClaimApproved}},
		{"I declined the force push.", []ClaimKind{ClaimDeclined}},
		{"Sent an interrupt to bench-codex.", []ClaimKind{ClaimStopped}},
		{"I cancelled the force push and told Codex never to force-push to main.", []ClaimKind{ClaimStopped, ClaimSent}},
		{"I've closed docs Claude.", []ClaimKind{ClaimClosed}},
		{"I've started a new Claude agent in the bench project.", []ClaimKind{ClaimStarted}},
		{"I've started Bench Claude on fixing those two broken links.", []ClaimKind{ClaimSent}},
		{"I verified that the tests pass.", []ClaimKind{ClaimVerified}},
		{"Done.", []ClaimKind{ClaimDone}},
		// Plans, questions, offers and negations claim nothing.
		{"I'll tell it to run the tests.", nil},
		{"Should I approve it?", nil},
		{"I haven't sent anything yet.", nil},
		{"Would you like me to stop it", nil},
		// A claim and its plan are two sentences.
		{"I've told bench-claude to run the tests, and I'll let you know when they're done.", []ClaimKind{ClaimSent}},
		{"I've started the tests on bench-claude and will let you know.", []ClaimKind{ClaimSent}},
		// A worker's own finish is news, not a claim.
		{"bench-claude finished writing the README.", nil},
		// Chinese, Simplified and the Traditional the voice model emits.
		{"好的，我已经让它运行测试了。", []ClaimKind{ClaimSent}},
		{"好的，我已经让 bench-codex 開始執行測試了。", []ClaimKind{ClaimSent}},
		{"已批准 bench-codex 运行测试。", []ClaimKind{ClaimApproved}},
		{"我已经拒绝了删除文件的请求。", []ClaimKind{ClaimDeclined}},
		{"已经停下来了。", []ClaimKind{ClaimStopped}},
		{"需要我让 Codex 修复这个 bug 吗？", nil},
		{"还没有发送。", nil},
		// Options appended to a reply are not claims.
		{"Codex wants to push. Options: Yes, proceed; No, I declined.", nil},
	}
	for _, c := range cases {
		if got := kinds(c.text); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: got %v, want %v", c.text, got, c.want)
		}
	}
}
