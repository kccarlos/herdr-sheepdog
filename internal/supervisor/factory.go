package supervisor

import (
	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
)

// Factory is the subset of *factory.Engine the Supervisor needs for the ask
// bridge and owner voice start/stop commands (design.md 12.3).
type Factory interface {
	Projects() []factory.ProjectView
	Asks(open bool) []flow.Ask
	MintProof(askID, option, inputKind, deviceID string) (factory.AnswerProof, error)
	Answer(askID, option string, proof factory.AnswerProof) error
	Start(folder, goal string, maxRunning int) error
	Stop(project string) error
	Changes() <-chan factory.Change
}

var _ Factory = (*factory.Engine)(nil)
