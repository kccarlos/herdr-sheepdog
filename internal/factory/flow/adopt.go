package flow

import (
	"fmt"
	"strconv"
)

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
	IsCrossRepository bool
}

// PRAdoptIn is what world fetched after pushing H to the attempt's branch (design.md 9.4).
type PRAdoptIn struct {
	Base string
	PRs  []PRCandidate
}

// PRAdoptRows is the first-match table deciding whether to adopt an existing
// PR, create one, or refuse (design.md 9.4, KB-GIT-2).
var PRAdoptRows = []Row[PRAdoptIn]{
	{ID: "P2", WaitsOn: "none", When: func(in PRAdoptIn) bool { return len(in.PRs) == 0 },
		Then: func(PRAdoptIn) Decision {
			return Decision{Asked: []string{PRCreate}, Why: "no PR for this branch: create one"}
		}},
	{ID: "P3", WaitsOn: "none", When: func(in PRAdoptIn) bool { return adoptablePR(in) != nil },
		Then: func(in PRAdoptIn) Decision {
			p := adoptablePR(in)
			return Decision{Asked: []string{PRAdopt, strconv.Itoa(p.Number)}, Why: fmt.Sprintf("adopt open same-repo PR #%d on %s", p.Number, in.Base)}
		}},
	{ID: "P4", WaitsOn: "none",
		Then: func(in PRAdoptIn) Decision {
			p := in.PRs[0]
			return Decision{Asked: []string{PRRefuse},
				Why: fmt.Sprintf("PR #%d (%s, base %s, fork %t) does not match open on %s: not adopted, no second PR",
					p.Number, p.State, p.Base, p.IsCrossRepository, in.Base)}
		}},
}

func adoptablePR(in PRAdoptIn) *PRCandidate {
	if in.Base == "" {
		return nil
	}
	for i := range in.PRs {
		p := &in.PRs[i]
		if p.State == PROpen && !p.IsCrossRepository && p.Base == in.Base {
			return p
		}
	}
	return nil
}

// AdoptPR runs PRAdoptRows and returns the decision (PRAdopt, PRCreate, or
// PRRefuse), the adopted PR number when PRAdopt, and the reason.
func AdoptPR(in PRAdoptIn) (action string, number int, why string) {
	_, d := FirstMatch(PRAdoptRows, in)
	if len(d.Asked) > 1 && d.Asked[0] == PRAdopt {
		n, _ := strconv.Atoi(d.Asked[1])
		return PRAdopt, n, d.Why
	}
	return d.Asked[0], 0, d.Why
}
