package postgres

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestUntrackedDiscoveryStateNullableActiveJob(t *testing.T) {
	base := map[string]any{"scope_id": "11111111-1111-4111-8111-111111111111", "requested_version": 1, "requested_sequence": 0, "completed_sequence": 0, "active_job_id": nil, "active_sequence": 0, "scheduled_scope_version": 0, "next_sweep_at": "2026-01-01T00:00:00Z"}
	raw, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := discoveryState(raw); err != nil || state.ActiveJobID != nil {
		t.Fatal("valid initial state cannot schedule its first job", state, err)
	}
	for _, name := range []string{"scope_id", "requested_version", "requested_sequence", "completed_sequence", "active_sequence", "scheduled_scope_version", "next_sweep_at"} {
		t.Run("null-"+name, func(t *testing.T) {
			fields := make(map[string]any, len(base))
			for k, v := range base {
				fields[k] = v
			}
			fields[name] = nil
			raw, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = discoveryState(raw); !errors.Is(err, ports.ErrUntrackedDiscoveryStale) {
				t.Fatal("non-null state field accepted null", err)
			}
		})
	}
	for _, name := range []string{"active_job_id", "requested_sequence"} {
		t.Run("missing-"+name, func(t *testing.T) {
			fields := make(map[string]any, len(base))
			for k, v := range base {
				fields[k] = v
			}
			delete(fields, name)
			raw, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = discoveryState(raw); !errors.Is(err, ports.ErrUntrackedDiscoveryStale) {
				t.Fatal("missing field accepted", err)
			}
		})
	}
	base["active_job_id"] = "22222222-2222-4222-8222-222222222222"
	raw, err = json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = discoveryState(raw); !errors.Is(err, ports.ErrUntrackedDiscoveryStale) {
		t.Fatal("job without valid sequence accepted", err)
	}
	base["requested_sequence"], base["active_sequence"], base["scheduled_scope_version"] = 1, 1, 1
	raw, err = json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = discoveryState(raw); err != nil {
		t.Fatal("valid active state rejected", err)
	}
}
