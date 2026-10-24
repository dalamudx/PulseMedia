package postgres_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
)

func globalRateAdmin(t *testing.T, f *globalRateFixture) *ports.Actor {
	t.Helper()
	id := uuid.New()
	f.exec(t, `INSERT INTO app_user(id,username,password_hash,role,is_active) VALUES($1,'native-rate-admin','fixture','admin',true)`, id)
	return &ports.Actor{ID: id.String(), Type: ports.ActorTypeUser, Role: ports.RoleAdmin}
}
func globalRateReadProtection(t *testing.T, f *globalRateFixture) string {
	t.Helper()
	var out string
	if err := f.pool.QueryRow(t.Context(), `SELECT jsonb_build_object('schedules',(SELECT jsonb_agg(to_jsonb(s) ORDER BY s.client_id) FROM download_global_rate_schedule s),'attempts',(SELECT jsonb_agg(to_jsonb(a) ORDER BY a.job_id) FROM download_global_rate_attempt a),'jobs',(SELECT jsonb_agg(to_jsonb(j) ORDER BY j.id) FROM job j),'clients',(SELECT jsonb_agg(to_jsonb(c) ORDER BY c.id) FROM download_client c))::text`).Scan(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestDownloadGlobalRatesReadOnlySnapshotPG(t *testing.T) {
	f := newGlobalRateFixture(t)
	actor := globalRateAdmin(t, f)
	reader := postgres.NewDownloadGlobalRatesOverviewStore(f.pool)
	empty, err := reader.ReadDownloadGlobalRatesSnapshot(t.Context(), actor)
	if err != nil || empty == nil || len(empty.Sources) != 1 || empty.Sources[0].Latest != nil || len(empty.History) != 0 {
		t.Fatal("unobserved configuration was misreported", err)
	}
	lease := f.claim(t)
	p := f.acquire(t, lease)
	if err = p.Finish(t.Context(), globalRateSample()); err != nil {
		t.Fatal(err)
	}
	p.Close()
	before := globalRateReadProtection(t, f)
	for range 3 {
		snapshot, e := reader.ReadDownloadGlobalRatesSnapshot(t.Context(), actor)
		if e != nil || snapshot == nil || len(snapshot.Sources) != 1 || len(snapshot.History) != 1 {
			t.Fatal("persisted sample read failed", e)
		}
		source := snapshot.Sources[0]
		if source.ID != f.client || source.Latest == nil || source.LastGood == nil || source.Latest.JobID != lease.ID || source.LastGood.JobID != lease.ID || *source.Latest.DownloadBytesPerSecond != 123 || *source.Latest.UploadBytesPerSecond != 456 || source.Latest.ObservedAt == nil || source.Latest.ObservedAt.After(snapshot.ObservedAt) {
			t.Fatal("sample identity, timestamp or values changed")
		}
		raw, e := json.Marshal(snapshot)
		if e != nil || strings.Contains(string(raw), "owned-native-reader") || strings.Contains(string(raw), "127.0.0.1/owned") {
			t.Fatal("private executable material entered read projection")
		}
	}
	if after := globalRateReadProtection(t, f); after != before {
		t.Fatal("read-only snapshot changed scheduler, attempts, clients or jobs")
	}
}

func TestDownloadGlobalRatesReadFailureAndConfigHistoryPG(t *testing.T) {
	f := newGlobalRateFixture(t)
	actor := globalRateAdmin(t, f)
	reader := postgres.NewDownloadGlobalRatesOverviewStore(f.pool)
	first := f.claim(t)
	p := f.acquire(t, first)
	if err := p.Finish(t.Context(), globalRateSample()); err != nil {
		t.Fatal(err)
	}
	p.Close()
	f.due(t)
	second := f.claim(t)
	p = f.acquire(t, second)
	if err := p.Finish(t.Context(), ports.DownloadGlobalRatesObservation{ObservedAt: time.Now().UTC(), Failure: ports.DownloadGlobalRatesAuth}); err != nil {
		t.Fatal(err)
	}
	p.Close()
	snapshot, err := reader.ReadDownloadGlobalRatesSnapshot(t.Context(), actor)
	if err != nil || snapshot == nil {
		t.Fatal(err)
	}
	source := snapshot.Sources[0]
	if source.Latest == nil || source.LastGood == nil || source.Latest.JobID != second.ID || source.LastGood.JobID != first.ID || source.Latest.DownloadBytesPerSecond != nil || source.Latest.State != "failed" || source.Latest.FailureCode != "auth_required" || *source.LastGood.DownloadBytesPerSecond != 123 {
		t.Fatal("failure lost original successful observation")
	}
	previousStamp := source.ConfigStamp
	f.exec(t, `UPDATE download_client SET username='changed-account' WHERE id=$1`, f.client)
	snapshot, err = reader.ReadDownloadGlobalRatesSnapshot(t.Context(), actor)
	if err != nil || snapshot == nil || snapshot.Sources[0].ConfigStamp == previousStamp || snapshot.Sources[0].Latest.ConfigStamp != previousStamp {
		t.Fatal("configuration boundary was rebound to old rates", err)
	}
}

func TestDownloadGlobalRatesReadCurrentAdministratorPG(t *testing.T) {
	f := newGlobalRateFixture(t)
	admin := globalRateAdmin(t, f)
	reader := postgres.NewDownloadGlobalRatesOverviewStore(f.pool)
	for _, kind := range []string{"anonymous", "member", "token", "unknown_user", "revoked_role", "inactive"} {
		t.Run(kind, func(t *testing.T) {
			f.exec(t, `UPDATE app_user SET role='admin',is_active=true WHERE id=$1`, uuid.MustParse(admin.ID))
			actor := *admin
			input := &actor
			switch kind {
			case "anonymous":
				input = nil
			case "member":
				actor.Role = ports.RoleMember
			case "token":
				actor.Scopes = []string{"read", "admin"}
			case "unknown_user":
				actor.ID = uuid.NewString()
			case "revoked_role":
				f.exec(t, `UPDATE app_user SET role='member' WHERE id=$1`, uuid.MustParse(admin.ID))
			case "inactive":
				f.exec(t, `UPDATE app_user SET is_active=false WHERE id=$1`, uuid.MustParse(admin.ID))
			}
			snapshot, err := reader.ReadDownloadGlobalRatesSnapshot(t.Context(), input)
			if !errors.Is(err, ports.ErrForbidden) || snapshot != nil {
				t.Fatal("current database account or session boundary bypassed", err)
			}
		})
	}
}

func TestDownloadGlobalRatesReadDatabaseFailurePG(t *testing.T) {
	f := newGlobalRateFixture(t)
	actor := globalRateAdmin(t, f)
	reader := postgres.NewDownloadGlobalRatesOverviewStore(f.pool)
	f.exec(t, `ALTER TABLE download_global_rate_schedule RENAME TO global_rate_schedule_unavailable`)
	snapshot, err := reader.ReadDownloadGlobalRatesSnapshot(t.Context(), actor)
	if !errors.Is(err, ports.ErrUnavailable) || snapshot != nil {
		t.Fatal("database failure became empty successful snapshot", err)
	}
	f.exec(t, `ALTER TABLE global_rate_schedule_unavailable RENAME TO download_global_rate_schedule`)
	if snapshot, err = reader.ReadDownloadGlobalRatesSnapshot(t.Context(), actor); err != nil || snapshot == nil {
		t.Fatal("read service failed after database recovery", err)
	}
}
