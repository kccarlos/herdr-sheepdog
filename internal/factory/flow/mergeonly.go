package flow

import "slices"

// Delta kinds of design.md 3.1: a delta from X to Y is merge_only when every
// first-parent commit is a clean merge of the base branch; else changes.
const (
	DeltaMergeOnly = "merge_only"
	DeltaChanges   = "changes"
	mergeParents   = 2
)

// MergeCommit is one first-parent commit in X..Y, fetched by world for the
// merge_only table (design.md 3.1, 9.4).
type MergeCommit struct {
	Parents      int  // parent count (2 for a standard merge)
	SecondInBase bool // second parent is an ancestor of the remote base
	TreeMatches  bool // commit tree equals git merge-tree of its two parents
}

// MergeOnlyIn is what world fetched for X..Y (design.md 3.1, 9.4).
type MergeOnlyIn struct {
	Rel     Rel
	Commits []MergeCommit
}

// MergeOnlyRows is the first-match table deciding merge_only vs changes
// (design.md 3.1, 9.4).
var MergeOnlyRows = []Row[MergeOnlyIn]{
	{ID: "M1", WaitsOn: "none", Stage: "none", When: func(in MergeOnlyIn) bool { return in.Rel != RelAhead },
		Then: func(MergeOnlyIn) Decision { return Decision{Asked: []string{DeltaChanges}, Why: "not ahead of X"} }},
	{ID: "M2", WaitsOn: "none", Stage: "none", When: func(in MergeOnlyIn) bool { return len(in.Commits) == 0 },
		Then: func(MergeOnlyIn) Decision { return Decision{Asked: []string{DeltaChanges}, Why: "no commits in range"} }},
	{ID: "M3", WaitsOn: "none", Stage: "none", When: func(in MergeOnlyIn) bool {
		return slices.ContainsFunc(in.Commits, func(c MergeCommit) bool { return c.Parents != mergeParents })
	}, Then: func(MergeOnlyIn) Decision {
		return Decision{Asked: []string{DeltaChanges}, Why: "non-merge commit in range"}
	}},
	{ID: "M4", WaitsOn: "none", Stage: "none", When: func(in MergeOnlyIn) bool {
		return slices.ContainsFunc(in.Commits, func(c MergeCommit) bool { return !c.SecondInBase })
	}, Then: func(MergeOnlyIn) Decision {
		return Decision{Asked: []string{DeltaChanges}, Why: "merge of non-base branch"}
	}},
	{ID: "M5", WaitsOn: "none", Stage: "none", When: func(in MergeOnlyIn) bool {
		return slices.ContainsFunc(in.Commits, func(c MergeCommit) bool { return !c.TreeMatches })
	}, Then: func(MergeOnlyIn) Decision {
		return Decision{Asked: []string{DeltaChanges}, Why: "merge tree edited by hand"}
	}},
	{ID: "M6", WaitsOn: "none", Stage: "none",
		Then: func(MergeOnlyIn) Decision {
			return Decision{Asked: []string{DeltaMergeOnly}, Why: "clean merges of base only"}
		}},
}

// IsMergeOnly reports whether MergeOnlyRows classifies in as merge_only.
func IsMergeOnly(in MergeOnlyIn) bool {
	_, d := FirstMatch(MergeOnlyRows, in)
	return len(d.Asked) > 0 && d.Asked[0] == DeltaMergeOnly
}
