package attention

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// fixtureMarks is herdr.Marks as the shared fixture spells it.
type fixtureMarks struct {
	Turn   uint64      `json:"turn"`
	Seen   *uint64     `json:"seen"`
	Pinned bool        `json:"pinned"`
	Word   herdr.State `json:"word"`
	Error  bool        `json:"error"`
}

func (f fixtureMarks) marks() herdr.Marks {
	return herdr.Marks{Turn: f.Turn, Seen: f.Seen, Pinned: f.Pinned, Word: f.Word, Error: f.Error}
}

type fixtureAgent struct {
	Name  string       `json:"name"`
	State herdr.State  `json:"state"`
	Marks fixtureMarks `json:"marks"`
}

type fixtureFile struct {
	Classify []struct {
		fixtureAgent
		Group  Group `json:"group"`
		Unread bool  `json:"unread"`
		Pinned bool  `json:"pinned"`
	} `json:"classify"`
	Orders []struct {
		Name     string         `json:"name"`
		Agents   []fixtureAgent `json:"agents"`
		Expected []string       `json:"expected"`
	} `json:"orders"`
}

func loadFixture(t *testing.T) fixtureFile {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "ios", "Tests", "Fixtures", "agent-attention.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f fixtureFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Classify) == 0 || len(f.Orders) == 0 {
		t.Fatal("fixture has no cases")
	}
	return f
}

// The shared fixture is the contract the phone and the panel are tested
// against too.
func TestClassifyFixture(t *testing.T) {
	for _, tc := range loadFixture(t).Classify {
		t.Run(tc.Name, func(t *testing.T) {
			got := Classify(tc.State, tc.Marks.marks())
			want := Attention{Group: tc.Group, Unread: tc.Unread, Pinned: tc.Pinned}
			if got != want {
				t.Fatalf("Classify(%s, %+v) = %+v, want %+v", tc.State, tc.Marks, got, want)
			}
		})
	}
}

func TestOrderFixture(t *testing.T) {
	for _, tc := range loadFixture(t).Orders {
		t.Run(tc.Name, func(t *testing.T) {
			items := make([]Item, len(tc.Agents))
			for i, a := range tc.Agents {
				items[i] = Item{Name: a.Name, Attention: Classify(a.State, a.Marks.marks())}
			}
			Sort(items)
			got := make([]string, len(items))
			for i, it := range items {
				got[i] = it.Name
			}
			if !slices.Equal(got, tc.Expected) {
				t.Fatalf("order = %v, want %v", got, tc.Expected)
			}
		})
	}
}

func TestClassify(t *testing.T) {
	seen := func(n uint64) *uint64 { return &n }
	tests := []struct {
		name  string
		state herdr.State
		marks herdr.Marks
		want  Attention
	}{
		{"blocked beats everything", herdr.StateBlocked, herdr.Marks{Turn: 3, Seen: seen(0), Pinned: true}, Attention{GroupNeedsYou, false, true}},
		{"error beats working", herdr.StateWorking, herdr.Marks{Error: true}, Attention{GroupNeedsYou, false, false}},
		{"working", herdr.StateWorking, herdr.Marks{Turn: 2, Seen: seen(0)}, Attention{GroupWorking, false, false}},
		{"idle unseen turn", herdr.StateIdle, herdr.Marks{Turn: 2, Seen: seen(1)}, Attention{GroupDone, true, false}},
		{"idle seen turn", herdr.StateIdle, herdr.Marks{Turn: 2, Seen: seen(2)}, Attention{GroupDone, false, false}},
		{"turn 0 seen 0", herdr.StateIdle, herdr.Marks{Seen: seen(0), Word: herdr.StateDone}, Attention{GroupDone, false, false}},
		{"no record, done word", herdr.StateIdle, herdr.Marks{Word: herdr.StateDone}, Attention{GroupDone, true, false}},
		{"no record, idle word", herdr.StateDone, herdr.Marks{Turn: 9, Word: herdr.StateIdle}, Attention{GroupDone, false, false}},
		{"unknown", herdr.StateUnknown, herdr.Marks{Pinned: true}, Attention{GroupNotRunning, false, true}},
		{"empty state", "", herdr.Marks{}, Attention{GroupNotRunning, false, false}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.state, tc.marks); got != tc.want {
				t.Fatalf("Classify = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestSortKeepsTiesInGivenOrder(t *testing.T) {
	// Same name, group and flags: nothing to tell them apart, so the given
	// order stays and rows do not jump between refreshes.
	items := []Item{
		{Name: "api", Attention: Attention{Group: GroupDone}},
		{Name: "API", Attention: Attention{Group: GroupDone}},
		{Name: "api", Attention: Attention{Group: GroupDone}},
	}
	want := slices.Clone(items)
	Sort(items)
	if !slices.Equal(items, want) {
		t.Fatalf("Sort changed ties: %+v", items)
	}
}

func TestCompareIsAnOrder(t *testing.T) {
	items := []Item{
		{"a", Attention{GroupNeedsYou, false, true}},
		{"a", Attention{GroupWorking, false, false}},
		{"b", Attention{GroupDone, true, true}},
		{"b", Attention{GroupDone, true, false}},
		{"a", Attention{GroupDone, false, false}},
		{"a", Attention{GroupNotRunning, false, false}},
	}
	for i, a := range items {
		for j, b := range items {
			ab, ba := Compare(a, b), Compare(b, a)
			if (ab < 0) != (ba > 0) || (ab == 0) != (ba == 0) {
				t.Fatalf("Compare(%d,%d)=%d but Compare(%d,%d)=%d", i, j, ab, j, i, ba)
			}
		}
	}
}
