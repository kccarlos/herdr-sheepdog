package herdr

import "testing"

func u(n uint64) *uint64 { return &n }

func TestMarksUnread(t *testing.T) {
	tests := []struct {
		name string
		m    Marks
		want bool
	}{
		{"unseen turn", Marks{Turn: 2, Seen: u(1)}, true},
		{"seen the turn", Marks{Turn: 2, Seen: u(2)}, false},
		{"seen above the turn", Marks{Turn: 1, Seen: u(4)}, false},
		{"seen 0 at turn 0", Marks{Seen: u(0)}, false},
		{"no record, done", Marks{Turn: 3, Word: StateDone}, true},
		{"no record, idle", Marks{Turn: 3, Word: StateIdle}, false},
		{"no record, working", Marks{Turn: 3, Word: StateWorking}, false},
		{"a record beats the word", Marks{Turn: 3, Seen: u(3), Word: StateDone}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.m.Unread(); got != tc.want {
				t.Fatalf("Unread() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSeenWrite(t *testing.T) {
	tests := []struct {
		name      string
		m         Marks
		turn      uint64
		wantSeen  uint64
		wantWrite bool
	}{
		{"raises", Marks{Turn: 3, Seen: u(1)}, 2, 2, true},
		{"raises to the turn", Marks{Turn: 3, Seen: u(1)}, 3, 3, true},
		{"clamps to the current turn", Marks{Turn: 3, Seen: u(1)}, 9, 3, true},
		{"never lowers", Marks{Turn: 3, Seen: u(3)}, 1, 0, false},
		{"same turn writes nothing", Marks{Turn: 3, Seen: u(2)}, 2, 0, false},
		{"already at the clamp writes nothing", Marks{Turn: 3, Seen: u(3)}, 9, 0, false},
		{"a missing record takes the turn", Marks{Turn: 3}, 3, 3, true},
		{"a missing record takes an older turn too", Marks{Turn: 3}, 1, 1, true},
		{"a missing record at turn 0 joins with 0", Marks{}, 0, 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			seen, write := tc.m.SeenWrite(tc.turn)
			if write != tc.wantWrite || (write && seen != tc.wantSeen) {
				t.Fatalf("SeenWrite(%d) = %d, %v; want %d, %v", tc.turn, seen, write, tc.wantSeen, tc.wantWrite)
			}
		})
	}
}

func TestUnreadWrite(t *testing.T) {
	tests := []struct {
		name      string
		m         Marks
		wantSeen  uint64
		wantWrite bool
	}{
		{"read agent", Marks{Turn: 4, Seen: u(4)}, 3, true},
		{"nothing finished yet", Marks{Seen: u(0)}, 0, false},
		{"turn 0 with no record", Marks{Word: StateIdle}, 0, false},
		{"already unread", Marks{Turn: 4, Seen: u(2)}, 0, false},
		{"unread by the backend's word", Marks{Turn: 4, Word: StateDone}, 0, false},
		{"read by the backend's word", Marks{Turn: 4, Word: StateIdle}, 3, true},
		{"turn 1", Marks{Turn: 1, Seen: u(1)}, 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			seen, write := tc.m.UnreadWrite()
			if write != tc.wantWrite || (write && seen != tc.wantSeen) {
				t.Fatalf("UnreadWrite() = %d, %v; want %d, %v", seen, write, tc.wantSeen, tc.wantWrite)
			}
		})
	}
}
