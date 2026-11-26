package web

import "github.com/kccarlos/herdr-sheepdog/internal/factory"

// The ask bridge fixture has questions but no project records.
func (*webFakeFactory) ProjectHeads() []factory.ProjectHead { return nil }

func (*webFakeFactory) Graph(string) (factory.GraphView, error) {
	return factory.GraphView{}, factory.ErrProjectNotFound
}

func (*webFakeFactory) LogLines(string, int, string, bool) ([]factory.LogEntry, error) {
	return nil, factory.ErrProjectNotFound
}
