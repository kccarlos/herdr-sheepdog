package hctest_test

import (
	"context"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
)

func TestRunRejectsInvalidTerminalName(t *testing.T) {
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	ctx := context.Background()

	for _, okName := range []string{"", "a", "coord-4cd6ea6a", "a0._-z", "a" + strings.Repeat("b", 31)} {
		if _, err := client.Run(ctx, hc.RunParams{Name: okName, Argv: []string{"sh"}}); err != nil {
			t.Fatalf("Run(%q) unexpected error: %v", okName, err)
		}
	}
	for _, bad := range []string{
		"Worker",
		"worker-4cd6ea6a-T1-1",
		"1worker",
		"-worker",
		"_worker",
		".worker",
		"bad/name",
		"bad name",
		strings.Repeat("a", 33),
	} {
		_, err := client.Run(ctx, hc.RunParams{Name: bad, Argv: []string{"sh"}})
		if err == nil || !strings.Contains(err.Error(), "bad name") {
			t.Fatalf("Run(%q) = %v, want bad name error", bad, err)
		}
	}
}
