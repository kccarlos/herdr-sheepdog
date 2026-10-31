package herdrcore_test

import (
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/herdrcore"
	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
	"github.com/kccarlos/herdr-sheepdog/internal/web"
)

// The backend offers the supervisor's Spawner and Closer and the web
// layer's phone-shell host; other optional capabilities are deliberately
// absent on hc. The assertions live here, not in the package, so herdrcore
// does not import its callers.
var (
	_ supervisor.Spawner = (*herdrcore.Backend)(nil)
	_ supervisor.Closer  = (*herdrcore.Backend)(nil)
	_ web.ShellHost      = (*herdrcore.Backend)(nil)
	_ web.ShellHost      = (*herdr.Client)(nil)
)
