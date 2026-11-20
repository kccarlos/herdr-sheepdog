package flow

import "fmt"

// Decision is what a row asks for and why, as the log shows it.
type Decision struct {
	Asked []string `json:"asked"`
	Why   string   `json:"why"`
}

// Row is one rule: a condition over F, who the task waits on, the stage it
// puts a task in, and what to ask for. A nil When always holds; only the last
// row ("no rule matched") may have one.
type Row[F any] struct {
	ID      string
	WaitsOn string
	Stage   string
	When    func(F) bool
	Then    func(F) Decision
}

// FirstMatch returns the first row whose condition holds, and its decision.
// A table that passes CheckTable always matches its last row.
func FirstMatch[F any](rows []Row[F], f F) (Row[F], Decision) {
	for _, r := range rows {
		if r.When == nil || r.When(f) {
			return r, r.Then(f)
		}
	}
	return Row[F]{}, Decision{}
}

// CheckTable fails unless every row has an ID, a WaitsOn and a Then,
// and only the last row is the catch-all (nil When).
func CheckTable[F any](rows []Row[F]) error {
	if len(rows) == 0 {
		return fmt.Errorf("empty table")
	}
	for i, r := range rows {
		last := i == len(rows)-1
		switch {
		case r.ID == "" || r.WaitsOn == "" || r.Then == nil:
			return fmt.Errorf("row %d (%q): needs an ID, a WaitsOn and a Then", i, r.ID)
		case last && r.When != nil:
			return fmt.Errorf("last row %q is not the catch-all \"no rule matched\"", r.ID)
		case !last && r.When == nil:
			return fmt.Errorf("row %q has no condition but is not last", r.ID)
		}
	}
	return nil
}
