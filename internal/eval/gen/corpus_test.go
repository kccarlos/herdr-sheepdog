package gen_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/eval"
	"github.com/kccarlos/herdr-sheepdog/internal/eval/gen"
)

const dataDir = "../../../testdata/eval"

// updating reports whether the golden corpus files should be rewritten
// (SHEEPDOG_CORPUS_UPDATE=1) instead of compared.
func updating() bool { return os.Getenv("SHEEPDOG_CORPUS_UPDATE") == "1" }

// TestExtractedCorpus regenerates testdata/eval/extracted/ from the real
// event logs when asked (SHEEPDOG_CORPUS_UPDATE=1 and
// SHEEPDOG_EVENTS_DIR=<data_dir>/events, and SHEEPDOG_CORPUS_REDACT for
// personal names a worker's screen showed); the logs are the user's
// own and not in the repository, so otherwise it is skipped.
func TestExtractedCorpus(t *testing.T) {
	dir := os.Getenv("SHEEPDOG_EVENTS_DIR")
	if !updating() || dir == "" {
		t.Skip("set SHEEPDOG_CORPUS_UPDATE=1 and SHEEPDOG_EVENTS_DIR to regenerate the extracted set")
	}
	for _, day := range []string{"2026-09-23", "2026-09-24"} {
		redact := eval.DefaultRedactions()
		for _, w := range strings.Split(os.Getenv("SHEEPDOG_CORPUS_REDACT"), ",") {
			if w = strings.TrimSpace(w); w != "" {
				redact = append(redact, w)
			}
		}
		cases, err := eval.ExtractDayFile(filepath.Join(dir, day+".jsonl"), eval.ExtractOptions{Redact: redact})
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dataDir, "extracted", day+".jsonl"), cases)
		t.Logf("%s: %d cases", day, len(cases))
	}
}

// TestExpandedCorpusUpToDate checks the checked-in expanded set is
// exactly what gen.CorpusOptions makes of the regressions and the picked
// extracted seeds: the generators are deterministic, and nobody edited
// the generated file by hand.
func TestExpandedCorpusUpToDate(t *testing.T) {
	seeds, err := eval.LoadSVPCases(filepath.Join(dataDir, "regressions.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	extracted, err := filepath.Glob(filepath.Join(dataDir, "extracted", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var picked []eval.SVPCase
	for _, f := range extracted {
		cs, err := eval.LoadSVPCases(f)
		if err != nil {
			t.Fatal(err)
		}
		picked = append(picked, cs...)
	}
	seeds = append(seeds, gen.PickSeeds(picked, gen.CorpusPerJob)...)
	svp := gen.Expand(seeds, gen.CorpusOptions)

	aseeds, err := eval.LoadAssistantCases(filepath.Join(dataDir, "regressions.assistant.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	asst := gen.ExpandAssistant(aseeds, gen.CorpusOptions)

	if updating() {
		writeFile(t, filepath.Join(dataDir, "expanded.jsonl"), svp)
		writeFile(t, filepath.Join(dataDir, "expanded.assistant.jsonl"), asst)
		t.Logf("expanded: %d SVP cases from %d seeds, %d assistant cases", len(svp), len(seeds), len(asst))
		return
	}
	compareFile(t, filepath.Join(dataDir, "expanded.jsonl"), svp)
	compareFile(t, filepath.Join(dataDir, "expanded.assistant.jsonl"), asst)
}

func encode[T any](t *testing.T, cases []T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := eval.WriteJSONL(&b, cases); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func writeFile[T any](t *testing.T, path string, cases []T) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encode(t, cases), 0o644); err != nil {
		t.Fatal(err)
	}
}

func compareFile[T any](t *testing.T, path string, cases []T) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, encode(t, cases)) {
		t.Errorf("%s is out of date with gen.CorpusOptions; regenerate with SHEEPDOG_CORPUS_UPDATE=1 go test ./internal/eval/gen", path)
	}
}
