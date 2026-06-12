package lifecycle

import (
	"fmt"

	"github.com/paivot-ai/pvg/internal/converge"
)

// FetchTools is the legacy `pvg fetch-tools` entry point, kept so existing
// callers (notably the paivot-graph Makefile) keep working. It now runs the
// channel-pinned convergence engine for the nd and vlt binaries plus the
// vlt skill, instead of chasing latest GitHub releases.
func FetchTools(force bool) error {
	fmt.Println("NOTE: pvg fetch-tools is deprecated -- use `pvg update` for full toolchain convergence.")
	rep, err := converge.Run(converge.Options{
		Force:    force,
		Tools:    []string{"nd", "vlt"},
		VltSkill: true,
	})
	if err != nil {
		return err
	}
	if rep.Failed {
		return fmt.Errorf("fetch-tools: one or more steps failed")
	}
	return nil
}
