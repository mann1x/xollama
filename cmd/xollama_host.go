package cmd

import (
	"context"
	"os"
	"sync"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/envconfig"
)

// Where the CLI connects when the operator has named no host.
//
// This runs once, from checkServerHeartbeat, which is the PreRunE of every
// command that needs a server. It deliberately does NOT live in
// api.ClientFromEnvironment, although that is where a client is built:
//
//   - a probe there would run on every one of the ~20 call sites in this file,
//     turning one resolution into twenty, and
//   - it would make client construction depend on what happens to be listening,
//     so a unit test's result would change depending on whether the developer
//     had a server running. TestClientFromEnvironment caught exactly that.
//
// Resolving once and recording the answer in the environment keeps
// ClientFromEnvironment a pure function of the environment, which is what every
// existing caller and test already assumes.
var resolveHostOnce sync.Once

// resolveHostErr is the refusal from the one resolution, replayed to every
// later caller so a second command in the same process cannot slip past it.
var resolveHostErr error

// resolveServerHost points this process at a running server when the operator
// named none, and refuses rather than drive a stock ollama.
//
// An explicit XOLLAMA_HOST is left exactly as it is: it is an instruction, and
// a command must fail against the host the operator named rather than quietly
// succeed against another one.
func resolveServerHost(ctx context.Context) error {
	resolveHostOnce.Do(func() {
		if envconfig.XollamaOnly("OLLAMA_HOST") != "" {
			return
		}
		resolved, err := api.ResolveHost(ctx)
		if err != nil {
			resolveHostErr = err
			return
		}
		// Recording it makes the choice sticky for every client this process
		// builds afterwards, including the ones inside `run`'s interactive
		// loop, without threading a URL through all of them.
		if resolved.String() != envconfig.ConnectableHost().String() {
			resolveHostErr = os.Setenv("XOLLAMA_HOST", resolved.String())
		}
	})
	return resolveHostErr
}
