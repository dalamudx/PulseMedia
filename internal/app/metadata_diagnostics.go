package app

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/tmdb"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"os"
)

// metadataDiagnosticSource shares the production provider's transport and budget.
type metadataDiagnosticSource struct{ provider *dynamicTMDBProvider }

func (s metadataDiagnosticSource) OpenDiagnostic(ctx context.Context) (ports.MetadataDiagnosticSession, error) {
	if s.provider == nil {
		return nil, ports.ErrUnavailable
	}
	environment := func() string {
		if key := os.Getenv("TMDB_API_KEY"); key != "" {
			return key
		}
		return os.Getenv("PULSE_TMDB_API_KEY")
	}
	return tmdb.NewDiagnosticSource(s.provider.settingsSvc, environment, s.provider.httpClient, s.provider.budget).OpenDiagnostic(ctx)
}
