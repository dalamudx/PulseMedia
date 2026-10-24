package postgres_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

func (f cloudScheduleFixture) site(t *testing.T, name, target, credential string, protocol ports.IndexerProtocol) uuid.UUID {
	t.Helper()
	endpoint, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	targetCipher, err := f.secret.EncryptSecret([]byte(target))
	if err != nil {
		t.Fatal(err)
	}
	keyCipher, err := f.secret.EncryptSecret([]byte(credential))
	if err != nil {
		t.Fatal(err)
	}
	store := postgres.NewIndexerManagementStore(f.p, idgen.New(), f.secret)
	out, err := store.MutateIndexer(t.Context(), f.owner, ports.IndexerMutation{Action: ports.IndexerCreate, Name: name, Configuration: ports.PreparedIndexerConfiguration{Protocol: protocol, DisplayURL: endpoint.Scheme + "://" + endpoint.Host, URLCiphertext: targetCipher, CredentialCiphertext: keyCipher, Categories: []int{2000}}}, name+".initial", managementHash)
	if err != nil || out == nil {
		t.Fatal("create configured site", err)
	}
	return out.Indexer.ID
}
func (f cloudScheduleFixture) claim(t *testing.T) ports.JobLease {
	t.Helper()
	f.seed(t, f.owner)
	f.enqueue(t)
	job, err := db.New(f.p).ClaimJob(t.Context(), db.ClaimJobParams{Column1: []string{ports.CookieCloudSyncJobKind}, Column2: "owned-cookiecloud-worker", Column3: 300000})
	if err != nil {
		t.Fatal("real queue claim", err)
	}
	return ports.JobLease{ID: job.ID, Owner: job.LeaseOwner.String, Token: job.LeaseToken}
}
func (f cloudScheduleFixture) permit(t *testing.T) (ports.CookieCloudSyncPermit, ports.JobLease) {
	t.Helper()
	lease := f.claim(t)
	p, err := f.runtime.Acquire(t.Context(), lease)
	if err != nil || p == nil {
		t.Fatal("physical permit acquire", err)
	}
	t.Cleanup(p.Close)
	if err = p.Reserve(t.Context()); err != nil {
		t.Fatal("single read reservation", err)
	}
	if err = p.Check(t.Context()); err != nil {
		t.Fatal("pre-read authority", err)
	}
	return p, lease
}
func (f cloudScheduleFixture) credential(t *testing.T, id uuid.UUID) (int64, []byte, string) {
	t.Helper()
	var version int64
	var cipher []byte
	if err := f.p.QueryRow(t.Context(), "SELECT config_version,api_key_encrypted FROM indexer WHERE id=$1", id).Scan(&version, &cipher); err != nil {
		t.Fatal(err)
	}
	plain, err := f.secret.DecryptSecret(cipher)
	if err != nil {
		t.Fatal("credential decrypt", err)
	}
	defer clear(plain)
	return version, cipher, string(plain)
}
func rootCloudCookie(name, value string) ports.CookieCloudCookie {
	yes := true
	return ports.CookieCloudCookie{Name: name, Value: value, Domain: "tracker.example", Path: "/", HostOnly: &yes, Secure: &yes, Session: &yes}
}
func cloudBackup(cookies ...ports.CookieCloudCookie) *ports.CookieCloudBackup {
	return &ports.CookieCloudBackup{Cookies: map[string][]ports.CookieCloudCookie{"untrusted-group": cookies}}
}
func cloudRowsUnchanged(t *testing.T, before, after map[string]string, allowed ...string) {
	t.Helper()
	for _, table := range allowed {
		delete(before, table)
		delete(after, table)
	}
	if !reflect.DeepEqual(before, after) {
		var changed []string
		for name, old := range before {
			if after[name] != old {
				changed = append(changed, name)
			}
		}
		t.Fatal("protected business rows changed", changed)
	}
}

func TestCookieCloudPermitEncryptedAtomicUpdatePG(t *testing.T) {
	f := newCloudEvidenceFixture(t)
	id := f.site(t, "eligible", "https://tracker.example/private/prefix?passkey=owned-passkey", "sid=old", ports.IndexerProtocolNexusPHP)
	unrelated := f.site(t, "unrelated", "https://other.example", "unrelated-old", ports.IndexerProtocolNexusPHP)
	unsupported := f.site(t, "unsupported", "https://tracker.example/torznab", "owned-api-key", ports.IndexerProtocolTorznab)
	configBefore, err := db.New(f.p).GetIndexerConfiguration(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	p, lease := f.permit(t)
	before := f.snapshot(t)
	if err = p.Finish(t.Context(), cloudBackup(rootCloudCookie("sid", "owned-cookie-value"), rootCloudCookie("uid", "42")), nil); err != nil {
		t.Fatal("atomic credential finish", err)
	}
	version, cipher, plain := f.credential(t, id)
	if version != 2 || plain != "sid=owned-cookie-value; uid=42" || bytes.Contains(cipher, []byte("owned-cookie-value")) {
		t.Fatal("credential/version/encryption contract failed")
	}
	configAfter, err := db.New(f.p).GetIndexerConfiguration(t.Context(), id)
	if err != nil || configAfter.ConfigurationVersion != 2 || !bytes.Equal(configBefore.UrlEncrypted, configAfter.UrlEncrypted) || configBefore.BehaviorVersion != configAfter.BehaviorVersion {
		t.Fatal("credential update changed URL or behavior", err)
	}
	for _, other := range []uuid.UUID{unrelated, unsupported} {
		v, _, _ := f.credential(t, other)
		if v != 1 {
			t.Fatal("unmatched or unsupported site changed")
		}
	}
	job, err := db.New(f.p).GetJobByID(t.Context(), lease.ID)
	if err != nil || job.State != "succeeded" {
		t.Fatal("job not atomically completed", err)
	}
	var result map[string]int
	if json.Unmarshal(job.Result, &result) != nil || result["updated"] != 1 || result["unmatched"] != 1 || result["unchanged"] != 0 || result["rejected"] != 0 {
		t.Fatal("safe result counts incorrect")
	}
	cloudRowsUnchanged(t, before, f.snapshot(t), "indexer", "indexer_configuration", "indexer_cookiecloud_credential", "audit_log", "outbox_event", "job", "cookiecloud_sync_attempt", "cookiecloud_sync_schedule")
	for _, table := range []string{"audit_log", "outbox_event", "job", "cookiecloud_sync_attempt", "idempotency_record"} {
		raw := f.snapshot(t)[table]
		for _, secret := range []string{"owned-cookie-value", "owned-password", "owned-passkey"} {
			if strings.Contains(raw, secret) {
				t.Fatal("plaintext secret entered persisted output", table)
			}
		}
	}
	p.Close()
	if second, err := f.runtime.Acquire(t.Context(), lease); err == nil {
		if second != nil {
			second.Close()
		}
		t.Fatal("finished task reacquired execution")
	}
}

func TestCookieCloudPermitPreservesUnusableAndUnchangedCookiesPG(t *testing.T) {
	for _, kind := range []string{"same-value", "empty", "wrong-host", "path-only", "ambiguous", "expired", "secure-http"} {
		t.Run(kind, func(t *testing.T) {
			f := newCloudEvidenceFixture(t)
			target := "https://tracker.example/private"
			if kind == "secure-http" {
				target = "http://tracker.example/private"
			}
			id := f.site(t, "preserved", target, "sid=owned-cookie-value", ports.IndexerProtocolNexusPHP)
			p, _ := f.permit(t)
			before := f.snapshot(t)
			_, cipherBefore, _ := f.credential(t, id)
			cookie := rootCloudCookie("sid", "owned-cookie-value")
			backup := cloudBackup(cookie)
			switch kind {
			case "empty":
				backup = cloudBackup()
			case "wrong-host":
				cookie.Domain = "other.example"
				backup = cloudBackup(cookie)
			case "path-only":
				cookie.Path = "/private"
				backup = cloudBackup(cookie)
			case "ambiguous":
				other := cookie
				other.Value = "different-owned-value"
				backup = cloudBackup(cookie, other)
			case "expired":
				no := false
				expired := float64(time.Now().Add(-time.Hour).Unix())
				cookie.Session = &no
				cookie.ExpirationDate = &expired
				backup = cloudBackup(cookie)
			}
			if err := p.Finish(t.Context(), backup, nil); err != nil {
				t.Fatal("safe preserved result", err)
			}
			v, cipher, _ := f.credential(t, id)
			if v != 1 || !bytes.Equal(cipherBefore, cipher) {
				t.Fatal("unusable/no-change cookie rewrote credential")
			}
			allowed := []string{"audit_log", "outbox_event", "job", "cookiecloud_sync_attempt", "cookiecloud_sync_schedule"}
			wantEvidence := 0
			if kind == "same-value" {
				allowed = append(allowed, "indexer_cookiecloud_credential")
				wantEvidence = 1
			}
			var evidenceCount int
			if err := f.p.QueryRow(t.Context(), "SELECT count(*) FROM indexer_cookiecloud_credential").Scan(&evidenceCount); err != nil || evidenceCount != wantEvidence {
				t.Fatal("unusable cookie produced validity evidence", evidenceCount, err)
			}
			cloudRowsUnchanged(t, before, f.snapshot(t), allowed...)
		})
	}
}

func TestCookieCloudPermitRejectsInvalidInitialAuthorityPG(t *testing.T) {
	for _, kind := range []string{"token", "owner", "configuration", "permission", "connection-cipher"} {
		t.Run(kind, func(t *testing.T) {
			f := newCloudEvidenceFixture(t)
			f.site(t, "initial", "https://tracker.example", "sid=old", ports.IndexerProtocolNexusPHP)
			lease := f.claim(t)
			switch kind {
			case "token":
				lease.Token++
			case "owner":
				lease.Owner = "foreign-owner"
			case "configuration":
				f.save(t, ports.CookieCloudConfigInput{ExpectedVersion: 1, Enabled: false, IntervalSeconds: 60}, "disable-before-acquire")
			case "permission":
				f.exec(t, "UPDATE app_user SET role='member' WHERE id=$1", f.owner.ID)
			case "connection-cipher":
				f.exec(t, "UPDATE cookiecloud_configuration SET connection_encrypted=$2,version=version+1,updated_at=clock_timestamp() WHERE owner_id=$1", f.owner.ID, []byte("owned-invalid-envelope"))
			}
			before := f.snapshot(t)
			p, err := f.runtime.Acquire(t.Context(), lease)
			if p != nil {
				p.Close()
			}
			if err == nil {
				t.Fatal("invalid authority acquired a permit")
			}
			if !reflect.DeepEqual(before, f.snapshot(t)) {
				t.Fatal("failed acquire changed database facts")
			}
		})
	}
}

func TestCookieCloudPermitRejectsLateAuthorityPG(t *testing.T) {
	for _, kind := range []string{"configuration", "permission", "source-version", "lease", "token"} {
		t.Run(kind, func(t *testing.T) {
			f := newCloudEvidenceFixture(t)
			id := f.site(t, "late", "https://tracker.example", "sid=old", ports.IndexerProtocolNexusPHP)
			p, lease := f.permit(t)
			switch kind {
			case "configuration":
				f.save(t, ports.CookieCloudConfigInput{ExpectedVersion: 1, Enabled: false, IntervalSeconds: 60}, "disabled-after-read")
			case "permission":
				f.exec(t, "UPDATE app_user SET role='member' WHERE id=$1", f.owner.ID)
			case "source-version":
				f.exec(t, `UPDATE indexer SET config_version=config_version+1,updated_at=clock_timestamp() WHERE id='`+id.String()+`'; UPDATE indexer_configuration SET configuration_version=configuration_version+1 WHERE indexer_id='`+id.String()+`'`)
			case "lease":
				f.exec(t, "UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", lease.ID)
			case "token":
				f.exec(t, "UPDATE job SET lease_token=lease_token+1 WHERE id=$1", lease.ID)
			}
			before := f.snapshot(t)
			if err := p.Check(t.Context()); err == nil {
				t.Fatal("late authority accepted by Check")
			}
			if err := p.Finish(t.Context(), cloudBackup(rootCloudCookie("sid", "owned-cookie-value")), nil); err == nil {
				t.Fatal("late authority published credentials")
			}
			if !reflect.DeepEqual(before, f.snapshot(t)) {
				t.Fatal("late result changed protected business rows")
			}
		})
	}
}

func TestCookieCloudPermitTailRollbackAndSameLeasePG(t *testing.T) {
	for _, table := range []string{"indexer_configuration", "audit_log", "outbox_event", "cookiecloud_sync_attempt", "job"} {
		t.Run(table, func(t *testing.T) {
			f := newCloudEvidenceFixture(t)
			id := f.site(t, "tail", "https://tracker.example", "sid=old", ports.IndexerProtocolNexusPHP)
			p, _ := f.permit(t)
			operation := "UPDATE"
			condition := "true"
			switch table {
			case "audit_log":
				operation = "INSERT"
				condition = "NEW.action='indexer.credentials_synchronized'"
			case "outbox_event":
				operation = "INSERT"
				condition = "NEW.event_type='cookiecloud.sync_succeeded'"
			case "cookiecloud_sync_attempt", "job":
				condition = "NEW.state='succeeded'"
			}
			f.exec(t, `CREATE FUNCTION cloud_permit_tail_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'owned cookie writeback tail'; END $$; CREATE TRIGGER cloud_permit_tail_fail BEFORE `+operation+` ON `+table+` FOR EACH ROW WHEN (`+condition+`) EXECUTE FUNCTION cloud_permit_tail_fail()`)
			before := f.snapshot(t)
			backup := cloudBackup(rootCloudCookie("sid", "owned-cookie-value"))
			if err := p.Finish(t.Context(), backup, nil); !errors.Is(err, ports.ErrUnavailable) {
				t.Fatal("writeback tail did not fail safely", err)
			}
			if !reflect.DeepEqual(before, f.snapshot(t)) {
				t.Fatal("credential/graph tail partially committed")
			}
			f.exec(t, "DROP TRIGGER cloud_permit_tail_fail ON "+table)
			if err := p.Finish(t.Context(), backup, nil); err != nil {
				t.Fatal("same lease failed to finish known rollback", err)
			}
			v, _, plain := f.credential(t, id)
			if v != 2 || plain != "sid=owned-cookie-value" {
				t.Fatal("known rollback replay was duplicated or lost")
			}
		})
	}
}

func TestCookieCloudPermitDeferredCommitFencesPG(t *testing.T) {
	for _, kind := range []string{"permission", "lease", "source-tamper", "cookie-expiry"} {
		t.Run(kind, func(t *testing.T) {
			f := newCloudEvidenceFixture(t)
			id := f.site(t, "deferred", "https://tracker.example", "sid=old", ports.IndexerProtocolNexusPHP)
			p, lease := f.permit(t)
			mutation := "UPDATE app_user SET role='member' WHERE id='" + f.owner.ID + "'::uuid;"
			switch kind {
			case "lease":
				mutation = "UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id='" + lease.ID.String() + "'::uuid;"
			case "source-tamper":
				mutation = "UPDATE indexer SET categories='[9999]'::jsonb WHERE id='" + id.String() + "'::uuid;"
			case "cookie-expiry":
				mutation = "PERFORM pg_sleep(3.2);"
			}
			f.exec(t, `CREATE FUNCTION cloud_permit_deferred_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+mutation+` RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER cloud_permit_deferred_fail AFTER UPDATE ON job DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.state='succeeded' AND OLD.state IS DISTINCT FROM NEW.state) EXECUTE FUNCTION cloud_permit_deferred_fail()`)
			cookie := rootCloudCookie("sid", "owned-cookie-value")
			if kind == "cookie-expiry" {
				now, err := db.New(f.p).GetSearchDatabaseTime(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				no := false
				expiry := float64(now.Time.Add(3*time.Second).UnixNano()) / 1e9
				cookie.Session = &no
				cookie.ExpirationDate = &expiry
			}
			before := f.snapshot(t)
			if err := p.Finish(t.Context(), cloudBackup(cookie), nil); err == nil {
				t.Fatal("deferred authority/clock tampering committed")
			}
			if !reflect.DeepEqual(before, f.snapshot(t)) {
				t.Fatal("deferred rejection failed full rollback")
			}
			var role string
			if err := f.p.QueryRow(t.Context(), "SELECT role FROM app_user WHERE id=$1", f.owner.ID).Scan(&role); err != nil || role != ports.RoleAdmin {
				t.Fatal("deferred permission mutation escaped rollback", err)
			}
		})
	}
}

func TestCookieCloudPermitFailurePreservesPriorCredentialsPG(t *testing.T) {
	f := newCloudEvidenceFixture(t)
	id := f.site(t, "failure", "https://tracker.example", "sid=old", ports.IndexerProtocolNexusPHP)
	p, lease := f.permit(t)
	before := f.snapshot(t)
	if err := p.Finish(t.Context(), nil, ports.NewRateLimitError(120*time.Second, "owned-private-cookie-detail")); err != nil {
		t.Fatal("safe failed attempt writeback", err)
	}
	version, _, plain := f.credential(t, id)
	if version != 1 || plain != "sid=old" {
		t.Fatal("failed backup changed credential")
	}
	job, err := db.New(f.p).GetJobByID(t.Context(), lease.ID)
	if err != nil || job.State != "dead" || !job.Error.Valid || job.Error.String != "rate_limited" {
		t.Fatal("safe failure state missing", err)
	}
	var delayed bool
	if err = f.p.QueryRow(t.Context(), `SELECT next_due_at>clock_timestamp()+interval '100 seconds' FROM cookiecloud_sync_schedule WHERE owner_id=$1`, f.owner.ID).Scan(&delayed); err != nil || !delayed {
		t.Fatal("rate-limit delay lost", err)
	}
	cloudRowsUnchanged(t, before, f.snapshot(t), "audit_log", "outbox_event", "job", "cookiecloud_sync_attempt", "cookiecloud_sync_schedule")
	for table, raw := range f.snapshot(t) {
		if strings.Contains(raw, "owned-private-cookie-detail") {
			t.Fatal("raw upstream detail persisted", table)
		}
	}
}
