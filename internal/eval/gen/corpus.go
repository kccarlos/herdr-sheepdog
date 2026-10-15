package gen

// CorpusOptions and CorpusPerJob are how the checked-in
// testdata/eval/expanded.jsonl and expanded.assistant.jsonl are generated
// (docs/dev/iteration-2.md WP0b: the runner has a corpus without generating
// it in CI). The equivalent command line, from the repository root:
//
//	sheepdog eval expand --in testdata/eval/regressions.jsonl \
//	  --pick 'testdata/eval/extracted/*.jsonl' --per-job 1 \
//	  --out testdata/eval/expanded.jsonl \
//	  --assistant-in testdata/eval/regressions.assistant.jsonl \
//	  --assistant-out testdata/eval/expanded.assistant.jsonl \
//	  --paraphrase --noise=3 --seed=1
//
// The corpus test fails when the checked-in files drift from this;
// SHEEPDOG_CORPUS_UPDATE=1 go test ./internal/eval/gen rewrites them.
var CorpusOptions = Options{Paraphrase: true, Noise: 3, Seed: 1}

// CorpusPerJob is --per-job for the extracted seeds.
const CorpusPerJob = 1
