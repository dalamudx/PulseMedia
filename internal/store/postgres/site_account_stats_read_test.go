package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
)

func newStatsReadFixture(t *testing.T) *statsFixture {
	t.Helper()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	v, e := postgres.Migrate(t.Context(), dsn, "../../../migrations")
	if e != nil || v != postgres.CurrentMigrationVersion {
		t.Fatal(v, e)
	}
	p, e := postgres.NewPool(t.Context(), dsn, postgres.DefaultPoolConfig())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(p.Close)
	f := &statsFixture{p: p, id: uuid.New(), dsn: dsn, secrets: &statsSecrets{key: bytes.Repeat([]byte{41}, 32)}}
	f.a = f.user(t)
	cookie, e := f.secrets.EncryptSecret([]byte("session=fixture"))
	if e != nil {
		t.Fatal(e)
	}
	root, e := f.secrets.EncryptSecret([]byte("https://nexus.test/root/"))
	if e != nil {
		t.Fatal(e)
	}
	f.exec(t, `INSERT INTO indexer(id,name,url,api_key_encrypted) VALUES($1,'stats','https://nexus.test',$2)`, f.id, cookie)
	f.exec(t, `INSERT INTO indexer_configuration(indexer_id,protocol,url_encrypted,behavior_version,configuration_version) VALUES($1,'nexusphp',$2,'explicit-v1',1)`, f.id, root)
	f.s = postgres.NewSiteAccountStatsConfigStore(p, idgen.New(), f.secrets)
	return f
}

func readStats(t *testing.T, f *statsFixture) *ports.SiteAccountStatsView {
	t.Helper()
	s := postgres.NewSiteAccountStatsStore(f.p, idgen.New(), nil)
	v, e := s.ReadSiteAccountStats(t.Context(), f.a, f.id)
	if e != nil {
		t.Fatal(e)
	}
	return v
}

// Prepare real jobs and fenced terminal evidence in one transaction, retaining all guards.
func prepareStatsReadAttempt(t *testing.T, f *statsFixture, state, raw, code string, age time.Duration) time.Time {
	t.Helper()
	ctx := t.Context()
	tx, e := f.p.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, e := tx.Exec(ctx, sql, args...); e != nil {
			t.Fatal(e)
		}
	}
	job := uuid.New()
	var cv, sv int64
	if e = tx.QueryRow(ctx, `SELECT version,source_config_version FROM site_account_stats_config WHERE indexer_id=$1`, f.id).Scan(&cv, &sv); e != nil {
		t.Fatal(e)
	}
	exec(`SELECT pg_advisory_xact_lock(hashtextextended('indexer:'||$1::uuid::text,0))`, f.id)
	js := "succeeded"
	if state == "failed" || state == "review_required" {
		js = "dead"
	}
	if state == "queued" {
		js = "queued"
	}
	exec(`INSERT INTO job(id,kind,state,payload,max_attempts,lease_owner,lease_token,lease_until) VALUES($1,'site_account_stats_collect',$2,jsonb_build_object('indexer_id',$3::uuid,'config_version',$4::bigint,'source_version',$5::bigint),1,'read-fixture',1,clock_timestamp()+interval '1 hour')`, job, js, f.id, cv, sv)
	exec(`INSERT INTO site_account_stats_schedule(indexer_id,config_version,state) VALUES($1,$2,'idle') ON CONFLICT(indexer_id) DO UPDATE SET config_version=$2,state='idle',active_job_id=NULL`, f.id, cv)
	exec(`SELECT set_config('pulse.stats_binding',pulse_stats_binding($1),true)`, f.id)
	var sample any
	if state == "succeeded" {
		sample = raw
	}
	var at time.Time
	if e = tx.QueryRow(ctx, `SELECT clock_timestamp()-$1::bigint*interval '1 microsecond'`, age.Microseconds()).Scan(&at); e != nil {
		t.Fatal(e)
	}
	if state == "queued" {
		exec(`INSERT INTO site_account_stats_attempt(job_id,indexer_id,config_version,source_version,state) VALUES($1,$2,$3,$4,'queued')`, job, f.id, cv, sv)
	} else {
		exec(`INSERT INTO site_account_stats_attempt(job_id,indexer_id,config_version,source_version,state,execution_owner,execution_token,started_at,finished_at,sample,failure_code) VALUES($1,$2,$3,$4,$5,'read-fixture',1,$6,$6,$7::jsonb,$8)`, job, f.id, cv, sv, state, at, sample, code)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	return at
}

func statsReadJSON(ratio string) string {
	return fmt.Sprintf(`{"account_id":"18446744073709551615","uploaded":{"value":"9007199254740993.001","unit":"TB","approximate":true},"downloaded":{"value":"0.00","unit":"KB","approximate":true},"ratio":%s,"bonus":"1234567890123456789.1"}`, ratio)
}

func TestSiteAccountStatsReadValuesPG(t *testing.T) {
	for _, ratio := range []string{`{"state":"finite","value":"0.000"}`, `{"state":"infinite"}`, `{"state":"undefined"}`, `{"state":"unknown"}`} {
		t.Run(ratio, func(t *testing.T) {
			f := newStatsReadFixture(t)
			f.save(t, f.input(), "first")
			v := readStats(t, f)
			if v.Stats != nil || v.ObservedAt != nil || !v.Stale || v.CollectionState != "never_collected" {
				t.Fatal(v)
			}
			raw := statsReadJSON(ratio)
			at := prepareStatsReadAttempt(t, f, "succeeded", raw, "", time.Second)
			v = readStats(t, f)
			if v.Stale || v.Stats == nil || !v.ObservedAt.Equal(at) || v.SampleConfigVersion != 1 || v.Stats.Downloaded.Value != "0.00" || !v.Stats.Downloaded.Approximate {
				t.Fatal(v)
			}
			var want ports.SiteAccountStats
			if e := json.Unmarshal([]byte(raw), &want); e != nil {
				t.Fatal(e)
			}
			if !reflect.DeepEqual(v.Stats, &want) {
				t.Fatal(v.Stats, want)
			}
			*v.Stats.Bonus = "changed"
			v.Stats.Uploaded.Value = "changed"
			v.Stats.Downloaded.Value = "changed"
			*v.ObservedAt = time.Time{}
			*v.LastAttemptAt = time.Time{}
			again := readStats(t, f)
			if !reflect.DeepEqual(again.Stats, &want) || !again.ObservedAt.Equal(at) {
				t.Fatal("aliased output")
			}
			prepareStatsReadAttempt(t, f, "failed", "", "auth_required", 0)
			v = readStats(t, f)
			if !v.Stale || v.CollectionState != "failed" || v.FailureCode != "auth_required" || !v.ObservedAt.Equal(at) || !reflect.DeepEqual(v.Stats, &want) {
				t.Fatal(v)
			}
		})
	}
}

func TestSiteAccountStatsReadBindingsPG(t *testing.T) {
	for _, change := range []string{"source", "config", "account", "disabled", "expired", "lifecycle", "review", "queued"} {
		t.Run(change, func(t *testing.T) {
			f := newStatsReadFixture(t)
			f.save(t, f.input(), "first")
			age := time.Second
			if change == "expired" {
				age = time.Hour
			}
			at := prepareStatsReadAttempt(t, f, "succeeded", statsReadJSON(`{"state":"infinite"}`), "", age)
			switch change {
			case "source":
				f.exec(t, `UPDATE indexer SET config_version=2 WHERE id=$1`, f.id)
			case "config", "account", "disabled":
				in := f.input()
				in.ExpectedVersion = 1
				in.IntervalSeconds = 901
				if change == "account" {
					in.ExpectedAccountID = "42"
				}
				if change == "disabled" {
					in.Enabled = false
				}
				f.save(t, in, "second")
			case "lifecycle":
				f.exec(t, `INSERT INTO indexer_lifecycle(indexer_id,enabled) VALUES($1,false) ON CONFLICT(indexer_id) DO UPDATE SET enabled=false`, f.id)
			case "review":
				prepareStatsReadAttempt(t, f, "review_required", "", "unavailable", 0)
			case "queued":
				prepareStatsReadAttempt(t, f, "queued", "", "", 0)
			}
			v := readStats(t, f)
			if change == "queued" {
				if v.CollectionState != "queued" || v.LastAttemptAt != nil {
					t.Fatal(v)
				}
				return
			}
			if !v.Stale {
				t.Fatal(v)
			}
			if change == "account" {
				if v.Stats != nil || v.ObservedAt != nil || v.SampleConfigVersion != 0 || v.SampleSourceVersion != 0 {
					t.Fatal(v)
				}
			} else if v.Stats == nil || !v.ObservedAt.Equal(at) {
				t.Fatal(v)
			}
		})
	}
}

func TestSiteAccountStatsReadAuthorizationPG(t *testing.T) {
	f := newStatsReadFixture(t)
	s := postgres.NewSiteAccountStatsStore(f.p, idgen.New(), nil)
	v, e := s.ReadSiteAccountStats(t.Context(), f.a, f.id)
	if e != nil || v != nil {
		t.Fatal(v, e)
	}
	_, e = s.ReadSiteAccountStats(t.Context(), f.a, uuid.New())
	statsErr(t, e, ports.ErrNotFound)
	for _, a := range []*ports.Actor{nil, {Type: ports.ActorTypeUser, ID: f.a.ID, Role: "member"}, {Type: ports.ActorTypeUser, ID: f.a.ID, Role: "admin", Scopes: []string{}}} {
		_, e = s.ReadSiteAccountStats(t.Context(), a, f.id)
		want := ports.ErrForbidden
		if a == nil {
			want = ports.ErrUnauthorized
		}
		statsErr(t, e, want)
	}
	f.exec(t, `UPDATE app_user SET is_active=false WHERE id=$1`, uuid.MustParse(f.a.ID))
	_, e = s.ReadSiteAccountStats(t.Context(), f.a, f.id)
	statsErr(t, e, ports.ErrForbidden)
}

func TestSiteAccountStatsReadCorruptionPG(t *testing.T) {
	for _, raw := range []string{`{"account_id":"18446744073709551615"}`, statsReadJSON(`{"state":"finite","value":"bad"}`), `{"account_id":"18446744073709551615","ratio":{"state":"unknown"},"unexpected":1}`} {
		t.Run(raw, func(t *testing.T) {
			f := newStatsReadFixture(t)
			f.save(t, f.input(), "first")
			prepareStatsReadAttempt(t, f, "succeeded", raw, "", time.Second)
			s := postgres.NewSiteAccountStatsStore(f.p, idgen.New(), nil)
			v, e := s.ReadSiteAccountStats(t.Context(), f.a, f.id)
			statsErr(t, e, ports.ErrUnavailable)
			if v != nil {
				t.Fatal(v)
			}
		})
	}
}

func TestSiteAccountStatsReadDatabaseFailurePG(t *testing.T) {
	f := newStatsReadFixture(t)
	f.p.Close()
	s := postgres.NewSiteAccountStatsStore(f.p, idgen.New(), nil)
	v, e := s.ReadSiteAccountStats(t.Context(), f.a, f.id)
	statsErr(t, e, ports.ErrUnavailable)
	if v != nil {
		t.Fatal(v)
	}
}

func TestSiteAccountStatsReadWaitPG(t *testing.T) {
	for _, column := range []string{"role='member'", "is_active=false"} {
		t.Run(column, func(t *testing.T) {
			f := newStatsReadFixture(t)
			f.save(t, f.input(), "first")
			tx, e := f.p.Begin(t.Context())
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			if _, e = tx.Exec(t.Context(), `UPDATE app_user SET `+column+` WHERE id=$1`, uuid.MustParse(f.a.ID)); e != nil {
				t.Fatal(e)
			}
			s := postgres.NewSiteAccountStatsStore(f.p, idgen.New(), nil)
			done := make(chan error, 1)
			go func() { _, err := s.ReadSiteAccountStats(t.Context(), f.a, f.id); done <- err }()
			deadline := time.Now().Add(3 * time.Second)
			for {
				var n int
				if e = f.p.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock'`).Scan(&n); e != nil {
					t.Fatal(e)
				}
				if n > 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("no real lock wait")
				}
				time.Sleep(5 * time.Millisecond)
			}
			if e = tx.Commit(t.Context()); e != nil {
				t.Fatal(e)
			}
			statsErr(t, <-done, ports.ErrForbidden)
		})
	}
}

func statsReadSnapshot(t *testing.T, f *statsFixture) map[string]string {
	t.Helper()
	rows, e := f.p.Query(t.Context(), `SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename`)
	if e != nil {
		t.Fatal(e)
	}
	var names []string
	for rows.Next() {
		var n string
		if e = rows.Scan(&n); e != nil {
			t.Fatal(e)
		}
		names = append(names, n)
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
	rows.Close()
	out := map[string]string{}
	for _, name := range names {
		var data string
		if e = f.p.QueryRow(t.Context(), `SELECT COALESCE(jsonb_agg(v ORDER BY v::text),'[]'::jsonb)::text FROM (SELECT to_jsonb(t) v FROM `+`"`+name+`"`+` t) x`).Scan(&data); e != nil {
			t.Fatal(e)
		}
		out[name] = data
	}
	return out
}

func TestSiteAccountStatsReadNoWritesPG(t *testing.T) {
	f := newStatsReadFixture(t)
	f.save(t, f.input(), "first")
	prepareStatsReadAttempt(t, f, "succeeded", statsReadJSON(`{"state":"infinite"}`), "", time.Second)
	before := statsReadSnapshot(t, f)
	for range 3 {
		readStats(t, f)
	}
	after := statsReadSnapshot(t, f)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("GET changed business tables or source budget")
	}
}

func TestSiteAccountStatsReadReceiptPG(t *testing.T) {
	for _, mode := range []string{"missing", "corrupt"} {
		t.Run(mode, func(t *testing.T) {
			f := newStatsReadFixture(t)
			if mode == "missing" {
				f.exec(t, `INSERT INTO site_account_stats_config(indexer_id,expected_account_id,profile,enabled,interval_seconds,version,source_config_version) VALUES($1,'42',$2,true,900,1,1)`, f.id, ports.NexusPHPClassicAccountStatsProfile)
			} else {
				f.save(t, f.input(), "first")
				f.exec(t, `UPDATE site_account_stats_config SET expected_account_id='42' WHERE indexer_id=$1`, f.id)
			}
			s := postgres.NewSiteAccountStatsStore(f.p, idgen.New(), nil)
			v, e := s.ReadSiteAccountStats(t.Context(), f.a, f.id)
			statsErr(t, e, ports.ErrUnavailable)
			if v != nil {
				t.Fatal(v)
			}
		})
	}
}

func TestSiteAccountStatsReadFuturePG(t *testing.T) {
	f := newStatsReadFixture(t)
	f.save(t, f.input(), "first")
	prepareStatsReadAttempt(t, f, "succeeded", statsReadJSON(`{"state":"undefined"}`), "", -time.Minute)
	s := postgres.NewSiteAccountStatsStore(f.p, idgen.New(), nil)
	v, e := s.ReadSiteAccountStats(t.Context(), f.a, f.id)
	statsErr(t, e, ports.ErrUnavailable)
	if v != nil {
		t.Fatal(v)
	}
}
