package main

import "testing"

func TestVersionString(t *testing.T) {
	tests := []struct {
		name                  string
		version, commit, date string
		want                  string
	}{
		{
			name:    "defaults",
			version: "dev",
			commit:  "none",
			date:    "unknown",
			want:    "sheepdog dev (commit none, built unknown)",
		},
		{
			name:    "release build",
			version: "v1.2.3",
			commit:  "abc1234",
			date:    "2026-09-17",
			want:    "sheepdog v1.2.3 (commit abc1234, built 2026-09-17)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := versionString(tt.version, tt.commit, tt.date)
			if got != tt.want {
				t.Errorf("versionString(%q, %q, %q) = %q, want %q",
					tt.version, tt.commit, tt.date, got, tt.want)
			}
		})
	}
}
