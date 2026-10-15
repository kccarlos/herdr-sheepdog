// Package docscheck holds the documentation checks that run in CI: every
// relative link in the Markdown files resolves, and every `sheepdog ...`
// command in the user docs' shell blocks names a subcommand and flags that
// the built binary's --help knows. It has tests only; no network is used.
package docscheck
