package cli

import (
	"context"
)

// RunBackend runs a specific backend regardless of cli.prefer. It is
// Run with the backend pinned.
func (r *Runner) RunBackend(ctx context.Context, backend Backend, workspace, question string) (Result, error) {
	return r.Run(ctx, Query{Backend: backend, Workspace: workspace, Question: question})
}
