// Package factory implements the Go side of sheepdog-factory: per-version
// unpacking of the embedded factory/ runtime, uv-managed CPython inspection and
// repair inside sheepdog's data directory, keeping the factory's tick loop
// alive as a herdr-core terminal (`hc run --keep`) once a project is started,
// the owner contract v1 client (`engine owner ... --json`), loopback AnswerProof
// persistence and verification, and prerequisite checks (`sheepdog factory doctor`).
package factory
