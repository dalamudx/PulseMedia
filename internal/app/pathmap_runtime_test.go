package app

import "testing"

func TestAppRunRemoteMappingSourceConfinement(t *testing.T) {
	for _, mode := range []string{"root", "conflict", "invalid", "symlink"} {
		t.Run(mode, func(t *testing.T) { runFullMoviePipelineWithMapping(t, true, mode) })
	}
}
