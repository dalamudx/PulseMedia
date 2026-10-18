package settings_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/command"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/settings"
	"sync"
	"testing"
)

func TestSettingReceiptBindsResource(t *testing.T) {
	s, q, a, _ := setupSettingsTest(t)
	ctx := context.Background()
	req := settings.UpdateSettingRequest{Value: map[string]any{"enabled": true}}
	if _, e := s.UpdateSetting(ctx, a, "counter.first", req, "resource-bound"); e != nil {
		t.Fatal(e)
	}
	if _, e := s.UpdateSetting(ctx, a, "counter.second", req, "resource-bound"); !errors.Is(e, command.ErrIdempotencyConflict) {
		t.Fatalf("cross-resource key replay accepted: %v", e)
	}
	if _, e := q.GetSetting(ctx, "counter.second"); e == nil {
		t.Fatal("second resource written")
	}
}
func TestSettingConcurrentOriginalReceipt(t *testing.T) {
	s, q, a, _ := setupSettingsTest(t)
	ctx := context.Background()
	req := settings.UpdateSettingRequest{Value: map[string]any{"enabled": true}}
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	cached := make(chan bool, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r, e := s.UpdateSetting(ctx, a, "counter.concurrent", req, "same-original-key")
			errs <- e
			if e == nil {
				cached <- r.FromCache
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	close(cached)
	for e := range errs {
		if e != nil {
			t.Errorf("concurrent identical request failed: %v", e)
		}
	}
	hits := 0
	for v := range cached {
		if v {
			hits++
		}
	}
	if hits != 7 {
		t.Errorf("cache hits %d want7", hits)
	}
	row, e := q.GetSetting(ctx, "counter.concurrent")
	if e != nil || row.Version != 1 {
		t.Errorf("version changed: %v", e)
	}
	logs, e := q.ListAuditLogs(ctx, db.ListAuditLogsParams{Limit: 20})
	if e != nil || len(logs) != 1 {
		t.Errorf("audit duplicated: %v count%d", e, len(logs))
	}
}
func TestSettingReplayChecksCurrentDatabasePermission(t *testing.T) {
	for _, change := range []string{"demoted", "disabled", "deleted"} {
		t.Run(change, func(t *testing.T) {
			pool, s, _, a, _ := setupSettingsPoolTest(t)
			ctx := context.Background()
			req := settings.UpdateSettingRequest{Value: map[string]any{"enabled": true}}
			if _, e := s.UpdateSetting(ctx, a, "counter.authority", req, "original-authority"); e != nil {
				t.Fatal(e)
			}
			statement := "UPDATE app_user SET role='member' WHERE id=$1"
			if change == "disabled" {
				statement = "UPDATE app_user SET is_active=false WHERE id=$1"
			}
			if change == "deleted" {
				statement = "DELETE FROM app_user WHERE id=$1"
			}
			if _, e := pool.Exec(ctx, statement, a.ID); e != nil {
				t.Fatal(e)
			}
			if _, e := s.UpdateSetting(ctx, a, "counter.authority", req, "original-authority"); !errors.Is(e, ports.ErrForbidden) {
				t.Errorf("stale actor recovered privileged receipt: %v", e)
			}
			if _, e := s.UpdateSetting(ctx, a, "counter.authority", settings.UpdateSettingRequest{Value: map[string]any{"enabled": false}, ExpectedVersion: 1}, "fresh-stale-actor"); !errors.Is(e, ports.ErrForbidden) {
				t.Errorf("stale actor wrote setting: %v", e)
			}
		})
	}
}
