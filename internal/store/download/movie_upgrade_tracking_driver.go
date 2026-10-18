package download

import "github.com/dalamudx/PulseMedia/internal/ports"

// Tracking shares the same native clients and physical execution budget as Add/R3.
func (r *Repository) MovieUpgradeTrackingDriver(v ports.MovieUpgradeTrackingQueryVerifier, i ports.MovieUpgradeTrackingMetainfoInspector, c ports.Clock) (ports.MovieUpgradeNativeTrackingDriver, error) {
	return NewMovieUpgradeNativeTrackingDriver(r.execution, v, i, c)
}
