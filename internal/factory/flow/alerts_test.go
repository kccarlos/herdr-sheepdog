package flow

import (
	"slices"
	"testing"
)

// TestPR5b8_ProjectRowsIncludeJ3AndJ5InOrder checks that the pure project rule
// table stays valid (no numeric literals, unique IDs, fallback last) and places
// J3 (stopping alert hold) before J4 and J5 (coordinator signed-out hold) after
// J4 and before J6/J7 (design.md section 6, KB-COO-31, KB-ENV-5).
func TestPR5b8_ProjectRowsIncludeJ3AndJ5InOrder(t *testing.T) {
	if err := CheckTable(ProjectRows); err != nil {
		t.Fatalf("CheckTable(ProjectRows): %v", err)
	}
	idx := func(id string) int {
		return slices.IndexFunc(ProjectRows, func(r Row[ProjectIn]) bool { return r.ID == id })
	}
	j3, j4, j5, j6, j7 := idx("J3"), idx("J4"), idx("J5"), idx("J6"), idx("J7")
	if j3 < 0 {
		t.Errorf("ProjectRows missing J3 (stopping alert hold); got IDs %v", projectRowIDs())
	}
	if j5 < 0 {
		t.Errorf("ProjectRows missing J5 (coordinator signed-out hold); got IDs %v", projectRowIDs())
	}
	if j3 >= 0 && j4 >= 0 && j3 >= j4 {
		t.Errorf("J3 index %d must precede J4 index %d (%v)", j3, j4, projectRowIDs())
	}
	if j5 >= 0 && j4 >= 0 && j5 <= j4 {
		t.Errorf("J5 index %d must come after J4 index %d (%v)", j5, j4, projectRowIDs())
	}
	if j5 >= 0 && j6 >= 0 && j5 >= j6 {
		t.Errorf("J5 index %d must precede J6 index %d (%v)", j5, j6, projectRowIDs())
	}
	if j5 >= 0 && j7 >= 0 && j5 >= j7 {
		t.Errorf("J5 index %d must precede J7 index %d (%v)", j5, j7, projectRowIDs())
	}
}

func projectRowIDs() []string {
	out := make([]string, len(ProjectRows))
	for i, r := range ProjectRows {
		out[i] = r.ID
	}
	return out
}
