package herdrcore_test

import (
	"github.com/kccarlos/herdr-sheepdog/internal/herdrcore"
	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
)

// The backend offers the supervisor's Spawner and Closer; the other
// optional capabilities are deliberately absent on hc. The assertions live
// here, not in the package, so herdrcore does not import the supervisor.
var (
	_ supervisor.Spawner = (*herdrcore.Backend)(nil)
	_ supervisor.Closer  = (*herdrcore.Backend)(nil)
)
