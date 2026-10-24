package postgres_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

func newCloudEvidenceFixture(t *testing.T) cloudScheduleFixture {
	t.Helper()
	f := newCloudScheduleFixture(t)
	history := repositoryMigrationsThrough(t, 110)
	if version, err := postgres.Migrate(t.Context(), f.dsn, history); err != nil || version != 110 {
		t.Fatal("credential evidence migration", version, err)
	}
	f.runtimeMigrations = history
	return f
}
func (f cloudScheduleFixture) sourceRow(t *testing.T, id uuid.UUID) db.Indexer {
	t.Helper()
	tx, err := f.p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tx.Rollback(context.WithoutCancel(t.Context())); err != nil {
			t.Error("source read rollback", err)
		}
	}()
	row, err := db.New(tx).LockRSSIndexer(t.Context(), id)
	if err != nil {
		t.Fatal("read current configured source", err)
	}
	return row
}
func (f cloudScheduleFixture) resolve(t *testing.T, id uuid.UUID) error {
	t.Helper()
	_, _, err := postgres.ResolveExecutableIndexerConfiguration(t.Context(), db.New(f.p), f.secret, f.sourceRow(t, id))
	return err
}
func (f cloudScheduleFixture) datedCookie(t *testing.T, until time.Duration) ports.CookieCloudCookie {
	t.Helper()
	now, err := db.New(f.p).GetSearchDatabaseTime(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	cookie := rootCloudCookie("sid", "owned-cookie-value")
	no := false
	expiry := float64(now.Time.Add(until).UnixNano()) / 1e9
	cookie.Session = &no
	cookie.ExpirationDate = &expiry
	return cookie
}
func TestCookieCloudCredentialEvidenceExecutionPG(t *testing.T) {
	for _, mode := range []string{"persistent", "session", "same-value"} {
		t.Run(mode, func(t *testing.T) {
			f := newCloudEvidenceFixture(t)
			prior := "sid=old"
			if mode == "same-value" {
				prior = "sid=owned-cookie-value"
			}
			id := f.site(t, "evidence", "https://tracker.example", prior, ports.IndexerProtocolNexusPHP)
			p, lease := f.permit(t)
			before := f.snapshot(t)
			cookie := rootCloudCookie("sid", "owned-cookie-value")
			if mode == "persistent" {
				cookie = f.datedCookie(t, time.Hour)
			}
			if err := p.Finish(t.Context(), cloudBackup(cookie), nil); err != nil {
				t.Fatal("commit credential evidence", err)
			}
			row := f.sourceRow(t, id)
			version := int64(2)
			if mode == "same-value" {
				version = 1
			}
			evidence, err := db.New(f.p).GetCookieCloudCredentialEvidence(t.Context(), db.GetCookieCloudCredentialEvidenceParams{IndexerID: id, SourceVersion: version})
			if err != nil || evidence.JobID != lease.ID || evidence.ValidUntil.Valid != (mode == "persistent") {
				t.Fatal("evidence not bound to original job/version/native expiry", err)
			}
			if err = f.resolve(t, id); err != nil {
				t.Fatal("fresh credentials cannot execute", err)
			}
			altered, err := f.secret.EncryptSecret([]byte("sid=forged-owned-value"))
			if err != nil {
				t.Fatal(err)
			}
			row.ApiKeyEncrypted = altered
			if _, _, err = postgres.ResolveExecutableIndexerConfiguration(t.Context(), db.New(f.p), f.secret, row); !errors.Is(err, ports.ErrUnavailable) {
				t.Fatal("same-version replaced credential evidence accepted", err)
			}
			cloudRowsUnchanged(t, before, f.snapshot(t), "indexer", "indexer_configuration", "indexer_cookiecloud_credential", "audit_log", "outbox_event", "job", "cookiecloud_sync_attempt", "cookiecloud_sync_schedule")
		})
	}
}
func TestCookieCloudCredentialExpiryRefreshPG(t *testing.T) {
	f := newCloudEvidenceFixture(t)
	id := f.site(t, "expiry", "https://tracker.example", "sid=old", ports.IndexerProtocolNexusPHP)
	p, _ := f.permit(t)
	if err := p.Finish(t.Context(), cloudBackup(f.datedCookie(t, 3*time.Second)), nil); err != nil {
		t.Fatal("initial expiry finish", err)
	}
	p.Close()
	if err := f.resolve(t, id); err != nil {
		t.Fatal("fresh expiry unexpectedly blocked", err)
	}
	f.exec(t, "SELECT pg_sleep(3.2)")
	before := f.snapshot(t)
	if err := f.resolve(t, id); !errors.Is(err, ports.ErrAuthRequired) {
		t.Fatal("expired CookieCloud credential remained executable", err)
	}
	if !reflect.DeepEqual(before, f.snapshot(t)) {
		t.Fatal("expiry guard changed facts")
	}
	// Establish an overdue schedule without reducing the production interval.
	f.exec(t, "UPDATE cookiecloud_sync_schedule SET next_due_at=clock_timestamp()-interval '1 second' WHERE owner_id=$1", f.owner.ID)
	f.enqueue(t)
	job, err := db.New(f.p).ClaimJob(t.Context(), db.ClaimJobParams{Column1: []string{ports.CookieCloudSyncJobKind}, Column2: "refresh-after-expiry", Column3: 300000})
	if err != nil {
		t.Fatal(err)
	}
	lease := ports.JobLease{ID: job.ID, Owner: job.LeaseOwner.String, Token: job.LeaseToken}
	refresh, err := f.runtime.Acquire(t.Context(), lease)
	if err != nil {
		t.Fatal("expired credential prevented authorized refresh", err)
	}
	defer refresh.Close()
	if err = refresh.Reserve(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = refresh.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = refresh.Finish(t.Context(), cloudBackup(rootCloudCookie("sid", "owned-fresh-value")), nil); err != nil {
		t.Fatal("refresh after expiry", err)
	}
	if err = f.resolve(t, id); err != nil {
		t.Fatal("new source version inherited old expiry", err)
	}
	var n int
	if err = f.p.QueryRow(t.Context(), "SELECT count(*) FROM indexer_cookiecloud_credential WHERE indexer_id=$1", id).Scan(&n); err != nil || n != 2 {
		t.Fatal("refresh erased original expiry evidence", n, err)
	}
}
func TestCookieCloudCredentialEvidenceTailAndStorageFailurePG(t *testing.T) {
	f := newCloudEvidenceFixture(t)
	id := f.site(t, "evidence-tail", "https://tracker.example", "sid=old", ports.IndexerProtocolNexusPHP)
	p, _ := f.permit(t)
	f.exec(t, `CREATE FUNCTION owned_cloud_evidence_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'owned validity tail'; END $$; CREATE TRIGGER owned_cloud_evidence_fail BEFORE INSERT ON indexer_cookiecloud_credential FOR EACH ROW EXECUTE FUNCTION owned_cloud_evidence_fail()`)
	before := f.snapshot(t)
	backup := cloudBackup(rootCloudCookie("sid", "owned-cookie-value"))
	if err := p.Finish(t.Context(), backup, nil); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal("evidence tail accepted", err)
	}
	if !reflect.DeepEqual(before, f.snapshot(t)) {
		t.Fatal("evidence failure partially committed credential")
	}
	f.exec(t, "DROP TRIGGER owned_cloud_evidence_fail ON indexer_cookiecloud_credential")
	if err := p.Finish(t.Context(), backup, nil); err != nil {
		t.Fatal("same lease recovery of evidence tail", err)
	}
	p.Close()
	f.exec(t, "ALTER TABLE indexer_cookiecloud_credential RENAME TO owned_cloud_evidence_unavailable")
	err := f.resolve(t, id)
	f.exec(t, "ALTER TABLE owned_cloud_evidence_unavailable RENAME TO indexer_cookiecloud_credential")
	if !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal("missing current-schema evidence treated as non-expiring", err)
	}
	if err = f.resolve(t, id); err != nil {
		t.Fatal("restored evidence cannot execute", err)
	}
}
func TestCookieCloudCredentialSchema110HistoryPG(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		f := newCloudEvidenceFixture(t)
		if version, err := postgres.Rollback(t.Context(), f.dsn, f.runtimeMigrations); err != nil || version != 109 {
			t.Fatal("empty 110 downgrade", version, err)
		}
		if version, err := postgres.Migrate(t.Context(), f.dsn, f.runtimeMigrations); err != nil || version != 110 {
			t.Fatal("empty 110 reinstall", version, err)
		}
	})
	t.Run("history", func(t *testing.T) {
		f := newCloudEvidenceFixture(t)
		f.site(t, "protected-evidence", "https://tracker.example", "sid=old", ports.IndexerProtocolNexusPHP)
		p, _ := f.permit(t)
		if err := p.Finish(t.Context(), cloudBackup(rootCloudCookie("sid", "owned-cookie-value")), nil); err != nil {
			t.Fatal(err)
		}
		p.Close()
		before := f.snapshot(t)
		if _, err := postgres.Rollback(t.Context(), f.dsn, f.runtimeMigrations); err == nil {
			t.Fatal("110 evidence dropped")
		}
		for _, query := range []string{"DELETE FROM indexer_cookiecloud_credential", "UPDATE indexer_cookiecloud_credential SET valid_until=NULL", "TRUNCATE indexer_cookiecloud_credential"} {
			if _, err := f.p.Exec(t.Context(), query); err == nil {
				t.Fatal("credential evidence history mutation accepted")
			}
		}
		if !reflect.DeepEqual(before, f.snapshot(t)) {
			t.Fatal("failed evidence history mutation changed facts")
		}
	})
}
