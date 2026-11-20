package flow

import "fmt"

// PR adoption decisions (design.md 9.4, KB-GIT-2).
const (
	PRAdopt  = "adopt"
	PRCreate = "create"
	PRRefuse = "refuse"
)

// PRCandidate is one PR returned by GitHub for the attempt's branch.
type PRCandidate struct {
	Number            int
	State             PRState
	Base              string
	Branch            string
	IsCrossRepository bool
}

// PRAdoptIn is what world fetched after pushing H to Branch (design.md 9.4).
type PRAdoptIn struct {
	Pushed bool
	Branch string
	Base   string
	PRs    []PRCandidate
}

// PRAdoptRows is the first-match table deciding whether to adopt an existing
// PR, create one, or refuse (design.md 9.4, KB-GIT-2).
var PRAdoptRows = []Row[PRAdoptIn]{
	{ID: "P1", WaitsOn: "none", When: func(in PRAdoptIn) bool { return !in.Pushed },
		Then: func(PRAdoptIn) Decision {
			return Decision{Asked: []string{PRRefuse}, Why: "cannot adopt or open a PR before the engine's own push"}
		}},
	{ID: "P2", WaitsOn: "none", When: func(in PRAdoptIn) bool { return len(in.PRs) == 0 },
		Then: func(PRAdoptIn) Decision {
			return Decision{Asked: []string{PRCreate}, Why: "no PR for this branch: create one"}
		}},
	{ID: "P3", WaitsOn: "none", When: func(in PRAdoptIn) bool { return adoptablePR(in) != nil },
		Then: func(in PRAdoptIn) Decision {
			p := adoptablePR(in)
			return Decision{Asked: []string{PRAdopt}, Why: fmt.Sprintf("adopt open same-repo PR #%d on %s", p.Number, in.Base)}
		}},
	{ID: "P4", WaitsOn: "none",
		Then: func(in PRAdoptIn) Decision {
			p := in.PRs[0]
			return Decision{Asked: []string{PRRefuse},
				Why: fmt.Sprintf("PR #%d (%s, base %s, branch %s, fork %t) does not match open on %s for %s: not adopted, no second PR",
					p.Number, p.State, p.Base, p.Branch, p.IsCrossRepository, in.Base, in.Branch)}
		}},
}

func adoptablePR(in PRAdoptIn) *PRCandidate {
	if in.Base == "" || in.Branch == "" {
		return nil
	}
	for i := range in.PRs {
		p := &in.PRs[i]
		if p.State == PROpen && !p.IsCrossRepository && p.Base == in.Base && p.Branch == in.Branch {
			return p
		}
	}
	return nil
}

// AdoptPR runs PRAdoptRows and returns the decision (PRAdopt, PRCreate, or
// PRRefuse), the adopted PR number when PRAdopt, and the reason.
func AdoptPR(in PRAdoptIn) (action string, number int, why string) {
	_, d := FirstMatch(PRAdoptRows, in)
	if len(d.Asked) > 0 && d.Asked[0] == PRAdopt {
		if p := adoptablePR(in); p != nil {
			return PRAdopt, p.Number, d.Why
		}
	}
	return d.Asked[0], 0, d.Why
}
