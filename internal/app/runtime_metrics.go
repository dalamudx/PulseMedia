package app

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
)

func (a *App) RuntimeMetricsReader() ports.RuntimeMetricsReader {
	return &appRuntimeMetricsReader{database: postgres.NewRuntimeMetricsReader(a.dbPool), stages: &a.stages}
}

type appRuntimeMetricsReader struct {
	database ports.RuntimeMetricsReader
	stages   *workStageRecorder
}

func (r *appRuntimeMetricsReader) ReadRuntimeMetrics(ctx context.Context) (ports.RuntimeMetrics, error) {
	snapshot, err := r.database.ReadRuntimeMetrics(ctx)
	if err != nil {
		return ports.RuntimeMetrics{}, err
	}
	snapshot.Stages = r.stages.snapshot()
	return snapshot, nil
}
