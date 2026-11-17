package factory

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
)

func TestClientStaysOffWhenNoProjectStarted(t *testing.T) {
	dataDir := t.TempDir()
	c := NewClient(ClientOptions{
		DataDir: dataDir,
		Home:    dataDir,
		Getenv:  func(string) string { return "" },
	})
	if c.Active() {
		t.Fatal("Client.Active() = true on empty data_dir, want false")
	}

	ctx := context.Background()
	projects, err := c.Projects(ctx)
	if err != nil || len(projects.Projects) != 0 {
		t.Fatalf("Projects: %+v, err=%v", projects, err)
	}
	asks, err := c.Asks(ctx, AsksFilter{Open: true})
	if err != nil || len(asks.Asks) != 0 {
		t.Fatalf("Asks: %+v, err=%v", asks, err)
	}
	rules, err := c.Rules(ctx, "")
	if err != nil || len(rules.Rules) != 0 {
		t.Fatalf("Rules: %+v, err=%v", rules, err)
	}
	alerts, err := c.Alerts(ctx, "")
	if err != nil || len(alerts.Alerts) != 0 {
		t.Fatalf("Alerts: %+v, err=%v", alerts, err)
	}
	if _, err := os.Stat(EnginesDir(dataDir)); !os.IsNotExist(err) {
		t.Fatalf("EnginesDir(%s) was created while inactive", dataDir)
	}
}

func TestClientCommandsAndErrorEnvelope(t *testing.T) {
	fx := loadContractFixture(t)
	var gotArgs [][]string

	c := NewClient(ClientOptions{
		DataDir: t.TempDir(),
		Getenv:  func(string) string { return "" },
		Exec: func(_ context.Context, args ...string) ([]byte, string, int, error) {
			gotArgs = append(gotArgs, append([]string(nil), args...))
			switch {
			case reflect.DeepEqual(args, []string{"owner", "projects", "--json"}):
				return fx.Valid.Projects, "", 0, nil
			case reflect.DeepEqual(args, []string{"owner", "asks", "--project", "beacon", "--open", "--json"}):
				raw, _ := json.Marshal(map[string]any{"protocol": 1, "asks": fx.Valid.AsksByKind[3:4]})
				return raw, "", 0, nil
			case reflect.DeepEqual(args, []string{"owner", "answer", "ask002", "--option", "1", "--proof", "prf-112233445566", "--json"}):
				return fx.Valid.Actions.Answer, "", 0, nil
			case reflect.DeepEqual(args, []string{"owner", "answer", "ask002", "--option", "1", "--proof", "prf-bad", "--json"}):
				raw, _ := json.Marshal(ErrorEnvelope{
					Protocol: 1,
					OK:       false,
					Code:     CodeProofRejected,
					Error:    "daemon rejected proof",
				})
				return raw, "", 1, nil
			case reflect.DeepEqual(args, []string{"owner", "say", "beacon", "Ship offline sync", "--json"}):
				return fx.Valid.Actions.Say, "", 0, nil
			default:
				t.Fatalf("unexpected CLI args: %v", args)
				return nil, "", 1, nil
			}
		},
	})

	ctx := context.Background()
	proj, err := c.Projects(ctx)
	if err != nil || len(proj.Projects) != 1 || proj.Projects[0].ID != "beacon" {
		t.Fatalf("Projects: %+v, err=%v", proj, err)
	}

	asks, err := c.Asks(ctx, AsksFilter{Project: "beacon", Open: true})
	if err != nil || len(asks.Asks) != 1 || asks.Asks[0].ID != "ask004" {
		t.Fatalf("Asks: %+v, err=%v", asks, err)
	}

	ans, err := c.AnswerIndex(ctx, "ask002", 1, "prf-112233445566")
	if err != nil || ans.Ask.ID != "ask002" {
		t.Fatalf("AnswerIndex: %+v, err=%v", ans, err)
	}

	_, err = c.AnswerIndex(ctx, "ask002", 1, "prf-bad")
	if !errors.Is(err, ErrProofRejected) || ErrorCode(err) != CodeProofRejected {
		t.Fatalf("AnswerIndex(rejected) err = %v (code=%q), want ErrProofRejected", err, ErrorCode(err))
	}

	say, err := c.Say(ctx, "beacon", "Ship offline sync")
	if err != nil || say.Coordinator != "term-4" {
		t.Fatalf("Say: %+v, err=%v", say, err)
	}
	if len(gotArgs) != 5 {
		t.Fatalf("got %d exec calls, want 5", len(gotArgs))
	}
}
