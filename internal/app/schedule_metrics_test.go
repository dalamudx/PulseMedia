package app

import (
	"bytes"
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestScheduleMetricsAggregateErrorsAndCancellation(t *testing.T) {
	var log bytes.Buffer
	a := &App{logger: slog.New(slog.NewTextHandler(&log, nil))}
	var next time.Time
	for range 4 {
		a.enqueueScheduled(t.Context(), ports.StageCookieCloudSchedule, &next, func(context.Context) error { return errors.New("must-not-log-private-detail") })
	}
	snapshot := a.stages.snapshot()[ports.StageCookieCloudSchedule]
	if snapshot.Returned != 4 || snapshot.Failures != 4 || snapshot.InFlight != 0 {
		t.Fatal(snapshot)
	}
	if strings.Count(log.String(), "periodic scheduling unavailable") != 1 || strings.Contains(log.String(), "private-detail") {
		t.Fatal("errors not aggregated or sanitized")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	a.enqueueScheduled(ctx, ports.StageCookieCloudSchedule, &next, func(ctx context.Context) error { return ctx.Err() })
	snapshot = a.stages.snapshot()[ports.StageCookieCloudSchedule]
	if snapshot.Returned != 5 || snapshot.Failures != 4 {
		t.Fatal("shutdown counted as scheduling failure")
	}
}
