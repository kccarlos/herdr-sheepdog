package main

import (
	"strings"
	"testing"
)

func TestWantsTier3(t *testing.T) {
	for _, tt := range []struct {
		args []string
		want bool
	}{
		{[]string{"--tier", "3", "--root", "x"}, true},
		{[]string{"--tier=3"}, true},
		{[]string{"-tier", "3"}, true},
		{[]string{"--tier", "2"}, false},
		{nil, false},
		{[]string{"--filter", "3"}, false},
		{[]string{"--", "--tier", "3"}, false},
	} {
		if got := wantsTier3(tt.args); got != tt.want {
			t.Errorf("wantsTier3(%v) = %v", tt.args, got)
		}
	}
}

func TestTier3UsageDocumentsVoice(t *testing.T) {
	var b strings.Builder
	printBenchTier3Usage(&b)
	for _, want := range []string{"--voice", "--tts-model", "--cache-dir", "--say"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("usage lacks %s", want)
		}
	}
}
