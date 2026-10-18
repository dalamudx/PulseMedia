package download

import "github.com/dalamudx/PulseMedia/internal/ports"

// Reuse the application's existing execution budget and client configuration.
func (r *Repository) MovieUpgradeObservationDriver(v ports.MovieUpgradeNativeQueryVerifier, clock ports.Clock) (ports.MovieUpgradeNativeObservationDriver, error) {
	return NewMovieUpgradeNativeObservationDriver(r.execution, v, clock)
}
