package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
	"testing"
	"time"
)

func historyFixture(t *testing.T) *checkinFixture {
	return newCheckinFixtureAt(t, "../../../migrations", postgres.CurrentMigrationVersion)
}

// Open-day fixtures have no execution or send permission and retain all PG guards.
func historyOpenDay(t *testing.T, f *checkinFixture, c ports.SiteCheckinConfig, day string) uuid.UUID {
	t.Helper()
	id, job := uuid.New(), uuid.New()
	raw, e := json.Marshal(c)
	if e != nil {
		t.Fatal(e)
	}
	f.exec(t, `INSERT INTO job(id,kind,dedupe_key,state,payload,max_attempts) VALUES($1::uuid,'site_checkin',$1::uuid::text,'queued','{}',3)`, job)
	f.exec(t, `INSERT INTO site_checkin_day(id,indexer_id,expected_account_id,profile,timezone,calendar_day,day_start,day_end,original_job_id,config_snapshot,config_version,source_version,binding_digest) VALUES($1,$2,$3,$4,$5,$6::date,$6::date::timestamp AT TIME ZONE $5,($6::date+1)::timestamp AT TIME ZONE $5,$7,$8,$9,$10,'fixture-open')`, id, f.id, c.ExpectedAccountID, c.Profile, c.Timezone, day, job, raw, c.Version, c.SourceConfigVersion)
	return id
}
func TestSiteCheckinHistoryEmptyAndMissingPG(t *testing.T) {
	f := historyFixture(t)
	s := postgres.NewSiteCheckinHistoryStore(f.p)
	out, e := s.ReadSiteCheckinHistory(t.Context(), f.a, f.id, 0, nil)
	if e != nil || out == nil || out.Config != nil || len(out.Rows) != 0 || out.Next != nil {
		t.Fatal(out, e)
	}
	_, e = s.ReadSiteCheckinHistory(t.Context(), f.a, uuid.New(), 20, nil)
	checkinErr(t, e, ports.ErrNotFound)
}
func TestSiteCheckinHistoryPaginationAndOldUIDPG(t *testing.T) {
	f := historyFixture(t)
	s := postgres.NewSiteCheckinHistoryStore(f.p)
	in := f.input()
	c := f.save(t, in, "first").Config
	first := historyOpenDay(t, f, c, "2026-10-19")
	in.ExpectedVersion = 1
	in.ExpectedAccountID = "2"
	in.Timezone = "UTC"
	c = f.save(t, in, "second").Config
	historyOpenDay(t, f, c, "2026-10-20")
	historyOpenDay(t, f, c, "2026-10-19")
	a := f.user(t)
	out, e := s.ReadSiteCheckinHistory(t.Context(), a, f.id, 1, nil)
	if e != nil || len(out.Rows) != 1 || out.Next == nil || out.Rows[0].CalendarDay != "2026-10-20" {
		t.Fatal(out, e)
	}
	seen := map[uuid.UUID]bool{out.Rows[0].ID: true}
	for out.Next != nil {
		out, e = s.ReadSiteCheckinHistory(t.Context(), a, f.id, 1, out.Next)
		if e != nil || len(out.Rows) != 1 || seen[out.Rows[0].ID] {
			t.Fatal(out, e)
		}
		seen[out.Rows[0].ID] = true
		if out.Rows[0].ID == first && (out.Rows[0].ExpectedAccountID != inExpectedUID || out.Rows[0].Timezone != "Asia/Shanghai") {
			t.Fatal(out)
		}
	}
	if len(seen) != 3 {
		t.Fatal(seen)
	}
}

const inExpectedUID = "18446744073709551615"

func TestSiteCheckinHistoryPermissionPG(t *testing.T) {
	f := historyFixture(t)
	s := postgres.NewSiteCheckinHistoryStore(f.p)
	for _, a := range []*ports.Actor{nil, {Type: ports.ActorTypeUser, ID: f.a.ID, Role: ports.RoleMember}, {Type: ports.ActorTypeSystem, ID: f.a.ID, Role: ports.RoleAdmin}, {Type: ports.ActorTypeService, ID: f.a.ID, Role: ports.RoleAdmin}} {
		out, e := s.ReadSiteCheckinHistory(t.Context(), a, f.id, 20, nil)
		if e == nil || out != nil {
			t.Fatal(out, e)
		}
	}
	f.exec(t, `UPDATE app_user SET is_active=false WHERE id=$1`, uuid.MustParse(f.a.ID))
	_, e := s.ReadSiteCheckinHistory(t.Context(), f.a, f.id, 20, nil)
	checkinErr(t, e, ports.ErrForbidden)
}
func TestSiteCheckinHistoryLockWaitRevocationPG(t *testing.T) {
	f := historyFixture(t)
	s := postgres.NewSiteCheckinHistoryStore(f.p)
	tx, e := f.p.Begin(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, e = tx.Exec(t.Context(), `UPDATE app_user SET role='member' WHERE id=$1`, uuid.MustParse(f.a.ID)); e != nil {
		t.Fatal(e)
	}
	result := make(chan error, 1)
	go func() { _, e := s.ReadSiteCheckinHistory(t.Context(), f.a, f.id, 20, nil); result <- e }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var n int
		e = f.p.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock'`).Scan(&n)
		if e != nil {
			t.Fatal(e)
		}
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("reader did not block on account")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if e = tx.Commit(t.Context()); e != nil {
		t.Fatal(e)
	}
	select {
	case e = <-result:
		checkinErr(t, e, ports.ErrForbidden)
	case <-time.After(3 * time.Second):
		t.Fatal("reader stuck")
	}
}
func TestSiteCheckinHistoryIsolationAndNoWritesPG(t *testing.T) {
	f := historyFixture(t)
	c := f.save(t, f.input(), "first").Config
	historyOpenDay(t, f, c, "2026-10-20")
	f.exec(t, `UPDATE restore_isolation SET isolated=true,restore_id='11111111-1111-4111-8111-111111111111' WHERE singleton`)
	s := postgres.NewSiteCheckinHistoryStore(f.p)
	var before, after string
	sql := `SELECT jsonb_build_object('days',(SELECT jsonb_agg(to_jsonb(d)) FROM site_checkin_day d),'jobs',(SELECT jsonb_agg(to_jsonb(j)) FROM job j),'config',(SELECT jsonb_agg(to_jsonb(c)) FROM site_checkin_config c),'audit',(SELECT count(*) FROM audit_log),'events',(SELECT count(*) FROM outbox_event))::text`
	if e := f.p.QueryRow(t.Context(), sql).Scan(&before); e != nil {
		t.Fatal(e)
	}
	out, e := s.ReadSiteCheckinHistory(t.Context(), f.a, f.id, 20, nil)
	if e != nil || len(out.Rows) != 1 {
		t.Fatal(out, e)
	}
	if e = f.p.QueryRow(t.Context(), sql).Scan(&after); e != nil || before != after {
		t.Fatal("read wrote state", e)
	}
}
func TestSiteCheckinHistoryCorruptAndSourceChangePG(t *testing.T) {
	f := historyFixture(t)
	c := f.save(t, f.input(), "first").Config
	id := historyOpenDay(t, f, c, "2026-10-20")
	s := postgres.NewSiteCheckinHistoryStore(f.p)
	f.exec(t, `UPDATE indexer SET config_version=config_version+1 WHERE id=$1`, f.id)
	out, e := s.ReadSiteCheckinHistory(t.Context(), f.a, f.id, 20, nil)
	if e != nil || len(out.Rows) != 1 {
		t.Fatal(out, e)
	}
	// Intentional tamper case: only the immutable-day trigger is suspended.
	f.exec(t, `ALTER TABLE site_checkin_day DISABLE TRIGGER site_checkin_day_history`)
	f.exec(t, `UPDATE site_checkin_day SET expected_account_id='001' WHERE id=$1`, id)
	f.exec(t, `ALTER TABLE site_checkin_day ENABLE TRIGGER site_checkin_day_history`)
	out, e = s.ReadSiteCheckinHistory(t.Context(), f.a, f.id, 20, nil)
	if out != nil || !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal(out, e)
	}
}
