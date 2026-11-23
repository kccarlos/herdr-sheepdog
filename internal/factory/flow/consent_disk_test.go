package flow

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// TestPR5b9_DiskFullPercentSettingDefaultRangeAndChanged checks the new
// disk_full_percent setting (design.md section 6, 10.1; CONFIG.md):
// default 90, allowed range 50–99, reported by Changed() when non-default,
// and defaulted on legacy state.json lacking the key.
func TestPR5b9_DiskFullPercentSettingDefaultRangeAndChanged(t *testing.T) {
	def := DefaultSettings()
	raw, err := json.Marshal(def)
	if err != nil {
		t.Fatalf("marshal DefaultSettings: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal DefaultSettings map: %v", err)
	}
	gotDefault, ok := fields["disk_full_percent"]
	if !ok {
		t.Fatalf("DefaultSettings() JSON missing \"disk_full_percent\" (want default 90); keys=%v", sortedKeys(fields))
	}
	if n, ok := gotDefault.(float64); !ok || int(n) != 90 {
		t.Fatalf("DefaultSettings().disk_full_percent = %v, want 90", gotDefault)
	}
	if err := def.Validate(); err != nil {
		t.Fatalf("DefaultSettings().Validate() = %v, want nil", err)
	}
	if slices.ContainsFunc(def.Changed(), func(s string) bool { return strings.HasPrefix(s, "disk_full_percent ") }) {
		t.Errorf("DefaultSettings().Changed() unexpectedly included disk_full_percent: %v", def.Changed())
	}

	setDiskFullPercent := func(t *testing.T, v int) Settings {
		t.Helper()
		s := DefaultSettings()
		b, err := json.Marshal(map[string]int{"disk_full_percent": v})
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, &s); err != nil {
			t.Fatalf("unmarshal disk_full_percent=%d: %v", v, err)
		}
		return s
	}

	for _, tc := range []struct {
		val int
		ok  bool
	}{
		{49, false},
		{50, true},
		{90, true},
		{99, true},
		{100, false},
		{0, false},
		{-1, false},
	} {
		s := setDiskFullPercent(t, tc.val)
		err := s.Validate()
		if (err == nil) != tc.ok {
			t.Errorf("disk_full_percent=%d: Validate() = %v, want ok=%v", tc.val, err, tc.ok)
		}
		if !tc.ok && err != nil && (!strings.Contains(err.Error(), "disk_full_percent") || !strings.Contains(err.Error(), "50") || !strings.Contains(err.Error(), "99")) {
			t.Errorf("disk_full_percent=%d: error %q should name disk_full_percent and range 50–99", tc.val, err.Error())
		}
	}

	custom := setDiskFullPercent(t, 85)
	if !slices.Contains(custom.Changed(), "disk_full_percent 85") {
		t.Errorf("Changed() for disk_full_percent=85 = %v, want to contain \"disk_full_percent 85\"", custom.Changed())
	}

	// Legacy state.json without disk_full_percent keeps default 90.
	legacy := DefaultSettings()
	if err := json.Unmarshal([]byte(`{"review_rounds":1}`), &legacy); err != nil {
		t.Fatal(err)
	}
	legacyBytes, _ := json.Marshal(legacy)
	var legacyFields map[string]any
	_ = json.Unmarshal(legacyBytes, &legacyFields)
	if n, ok := legacyFields["disk_full_percent"].(float64); !ok || int(n) != 90 {
		t.Errorf("legacy unmarshal disk_full_percent = %v, want 90", legacyFields["disk_full_percent"])
	}
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
