package factory

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type contractFixture struct {
	About      string      `json:"about"`
	Protocol   int         `json:"protocol"`
	AskKinds   []AskKind   `json:"ask_kinds"`
	RuleTypes  []RuleType  `json:"rule_types"`
	AlertKinds []AlertKind `json:"alert_kinds"`
	Valid      struct {
		Projects         json.RawMessage   `json:"projects"`
		AsksByKind       []json.RawMessage `json:"asks_by_kind"`
		Digest           json.RawMessage   `json:"digest"`
		Rules            json.RawMessage   `json:"rules"`
		GraphProvisional json.RawMessage   `json:"graph_provisional"`
		Alerts           json.RawMessage   `json:"alerts"`
		AnswerProof      json.RawMessage   `json:"answer_proof"`
		Actions          struct {
			Answer               json.RawMessage `json:"answer"`
			Say                  json.RawMessage `json:"say"`
			ProposeRule          json.RawMessage `json:"propose_rule"`
			RevokeRuleDelegation json.RawMessage `json:"revoke_rule_delegation"`
			RevokeRuleGated      json.RawMessage `json:"revoke_rule_gated"`
		} `json:"actions"`
	} `json:"valid"`
	Invalid []struct {
		Name      string          `json:"name"`
		Target    string          `json:"target"`
		ErrorCode string          `json:"error_code"`
		Payload   json.RawMessage `json:"payload"`
	} `json:"invalid"`
}

func loadContractFixture(t *testing.T) contractFixture {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "factory", "owner-contract-v1.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var fx contractFixture
	if err := decodeStrictJSON(raw, &fx, CodeUnexpectedKey, ErrUnexpectedKey); err != nil {
		t.Fatalf("decode fixture %s: %v", path, err)
	}
	return fx
}

func assertJSONRoundTrip(t *testing.T, label string, original []byte, decoded any) {
	t.Helper()
	encoded, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("%s: marshal round-trip: %v", label, err)
	}
	var want, got any
	if err := json.Unmarshal(original, &want); err != nil {
		t.Fatalf("%s: unmarshal original: %v", label, err)
	}
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("%s: unmarshal round-trip: %v", label, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: round-trip mismatch\ngot:  %s\nwant: %s", label, string(encoded), string(original))
	}
}

func TestOwnerContractV1Fixture(t *testing.T) {
	fx := loadContractFixture(t)

	if fx.Protocol != ProtocolVersion {
		t.Fatalf("fixture protocol = %d, want %d", fx.Protocol, ProtocolVersion)
	}
	if !reflect.DeepEqual(fx.AskKinds, AskKinds()) {
		t.Fatalf("ask_kinds = %v, want %v", fx.AskKinds, AskKinds())
	}
	if !reflect.DeepEqual(fx.RuleTypes, RuleTypes()) {
		t.Fatalf("rule_types = %v, want %v", fx.RuleTypes, RuleTypes())
	}
	if !reflect.DeepEqual(fx.AlertKinds, AlertKinds()) {
		t.Fatalf("alert_kinds = %v, want %v", fx.AlertKinds, AlertKinds())
	}

	t.Run("valid/projects", func(t *testing.T) {
		resp, err := DecodeProjectsResponse(fx.Valid.Projects)
		if err != nil {
			t.Fatalf("DecodeProjectsResponse: %v", err)
		}
		assertJSONRoundTrip(t, "projects", fx.Valid.Projects, resp)
	})

	t.Run("valid/asks_by_kind", func(t *testing.T) {
		seenKinds := map[AskKind]bool{}
		for i, raw := range fx.Valid.AsksByKind {
			ask, err := DecodeAsk(raw)
			if err != nil {
				t.Fatalf("asks_by_kind[%d]: DecodeAsk: %v", i, err)
			}
			seenKinds[ask.Kind] = true
			assertJSONRoundTrip(t, string(ask.Kind), raw, ask)
		}
		for _, k := range AskKinds() {
			if !seenKinds[k] {
				t.Errorf("fixture asks_by_kind missing kind %q", k)
			}
		}

		envelope, err := json.Marshal(map[string]any{
			"protocol": ProtocolVersion,
			"asks":     fx.Valid.AsksByKind,
		})
		if err != nil {
			t.Fatalf("marshal asks envelope: %v", err)
		}
		resp, err := DecodeAsksResponse(envelope)
		if err != nil {
			t.Fatalf("DecodeAsksResponse: %v", err)
		}
		assertJSONRoundTrip(t, "asks_envelope", envelope, resp)
	})

	t.Run("valid/digest", func(t *testing.T) {
		resp, err := DecodeDigestResponse(fx.Valid.Digest)
		if err != nil {
			t.Fatalf("DecodeDigestResponse: %v", err)
		}
		assertJSONRoundTrip(t, "digest", fx.Valid.Digest, resp)
	})

	t.Run("valid/rules", func(t *testing.T) {
		resp, err := DecodeRulesResponse(fx.Valid.Rules)
		if err != nil {
			t.Fatalf("DecodeRulesResponse: %v", err)
		}
		seenTypes := map[RuleType]bool{}
		sawText := false
		for _, r := range resp.Rules {
			if r.Class == "typed" && r.Type != nil {
				seenTypes[*r.Type] = true
			}
			if r.Class == "text" {
				sawText = true
			}
		}
		for _, rt := range RuleTypes() {
			if !seenTypes[rt] {
				t.Errorf("fixture rules missing typed rule %q", rt)
			}
		}
		if !sawText {
			t.Error("fixture rules missing text rule")
		}
		assertJSONRoundTrip(t, "rules", fx.Valid.Rules, resp)
	})

	t.Run("valid/alerts", func(t *testing.T) {
		resp, err := DecodeAlertsResponse(fx.Valid.Alerts)
		if err != nil {
			t.Fatalf("DecodeAlertsResponse: %v", err)
		}
		seenKinds := map[AlertKind]bool{}
		for _, a := range resp.Alerts {
			seenKinds[a.Kind] = true
		}
		for _, ak := range AlertKinds() {
			if !seenKinds[ak] {
				t.Errorf("fixture alerts missing alert kind %q", ak)
			}
		}
		assertJSONRoundTrip(t, "alerts", fx.Valid.Alerts, resp)
	})

	t.Run("valid/answer_proof", func(t *testing.T) {
		proof, err := DecodeAnswerProof(fx.Valid.AnswerProof)
		if err != nil {
			t.Fatalf("DecodeAnswerProof: %v", err)
		}
		assertJSONRoundTrip(t, "answer_proof", fx.Valid.AnswerProof, proof)
	})

	t.Run("valid/actions", func(t *testing.T) {
		ans, err := DecodeAnswerResponse(fx.Valid.Actions.Answer)
		if err != nil {
			t.Fatalf("DecodeAnswerResponse: %v", err)
		}
		assertJSONRoundTrip(t, "actions.answer", fx.Valid.Actions.Answer, ans)

		say, err := DecodeSayResponse(fx.Valid.Actions.Say)
		if err != nil {
			t.Fatalf("DecodeSayResponse: %v", err)
		}
		assertJSONRoundTrip(t, "actions.say", fx.Valid.Actions.Say, say)

		prop, err := DecodeProposeRuleResponse(fx.Valid.Actions.ProposeRule)
		if err != nil {
			t.Fatalf("DecodeProposeRuleResponse: %v", err)
		}
		assertJSONRoundTrip(t, "actions.propose_rule", fx.Valid.Actions.ProposeRule, prop)

		revDel, err := DecodeRevokeRuleResponse(fx.Valid.Actions.RevokeRuleDelegation)
		if err != nil {
			t.Fatalf("DecodeRevokeRuleResponse(delegation): %v", err)
		}
		assertJSONRoundTrip(t, "actions.revoke_rule_delegation", fx.Valid.Actions.RevokeRuleDelegation, revDel)

		revGated, err := DecodeRevokeRuleResponse(fx.Valid.Actions.RevokeRuleGated)
		if err != nil {
			t.Fatalf("DecodeRevokeRuleResponse(gated): %v", err)
		}
		assertJSONRoundTrip(t, "actions.revoke_rule_gated", fx.Valid.Actions.RevokeRuleGated, revGated)
	})

	t.Run("valid/graph_provisional", func(t *testing.T) {
		// Provisional Phase 4 shape: decoded without strict extra-key rejection until SDF-32.
		var withExtra map[string]any
		if err := json.Unmarshal(fx.Valid.GraphProvisional, &withExtra); err != nil {
			t.Fatalf("unmarshal graph_provisional: %v", err)
		}
		withExtra["future_phase4_field"] = true
		aug, err := json.Marshal(withExtra)
		if err != nil {
			t.Fatalf("marshal augmented graph_provisional: %v", err)
		}
		g, err := DecodeGraphResponse(aug)
		if err != nil {
			t.Fatalf("DecodeGraphResponse: %v", err)
		}
		if g.Project != "beacon" || g.CriticalPath == nil || len(g.Tasks) != 3 {
			t.Fatalf("unexpected decoded GraphResponse: %+v", g)
		}
	})

	if len(fx.Invalid) == 0 {
		t.Fatal("fixture invalid cases list is empty")
	}
	for _, tc := range fx.Invalid {
		t.Run("invalid/"+tc.Name, func(t *testing.T) {
			var err error
			switch tc.Target {
			case "ask":
				_, err = DecodeAsk(tc.Payload)
			case "rule":
				_, err = DecodeRule(tc.Payload)
			case "alert":
				_, err = DecodeAlert(tc.Payload)
			case "answer_proof":
				_, err = DecodeAnswerProof(tc.Payload)
			default:
				t.Fatalf("unknown invalid case target %q", tc.Target)
			}
			if err == nil {
				t.Fatalf("expected error %q for %s, got nil", tc.ErrorCode, tc.Name)
			}
			gotCode := ErrorCode(err)
			if gotCode != tc.ErrorCode {
				t.Fatalf("ErrorCode(%v) = %q, want %q", err, gotCode, tc.ErrorCode)
			}
			if tc.Target == "ask" && tc.ErrorCode == CodeUnknownKind && !errors.Is(err, ErrUnknownAskKind) {
				t.Fatalf("expected errors.Is(err, ErrUnknownAskKind), got %v", err)
			}
			if tc.Target == "alert" && tc.ErrorCode == CodeUnknownKind && !errors.Is(err, ErrUnknownAlertKind) {
				t.Fatalf("expected errors.Is(err, ErrUnknownAlertKind), got %v", err)
			}
			if tc.ErrorCode == CodeProofRejected && !errors.Is(err, ErrProofRejected) {
				t.Fatalf("expected errors.Is(err, ErrProofRejected), got %v", err)
			}
		})
	}
}
