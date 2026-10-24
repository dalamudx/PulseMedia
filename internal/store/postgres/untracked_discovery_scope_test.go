package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

func TestUntrackedDiscoveryScopeRevocationAndMaterialPG(t *testing.T) {
	for _, kind := range []string{"role", "inactive", "disabled", "scope_version", "client_version", "client_digest", "mapping", "invalid_mapping", "root_path", "generation", "marker", "offline", "baseline"} {
		t.Run(kind, func(t *testing.T) {
			f := newUntrackedDiscoveryFixture(t)
			f.request(t)
			c := f.claim(t)
			switch kind {
			case "role":
				f.exec(t, `UPDATE app_user SET role='member' WHERE id=$1`, uuid.MustParse(f.a.ID))
			case "inactive":
				f.exec(t, `UPDATE app_user SET is_active=false WHERE id=$1`, uuid.MustParse(f.a.ID))
			case "disabled", "scope_version":
				in := f.in
				in.ExpectedVersion = 1
				in.Enabled = kind != "disabled"
				f.save(t, in, "changed-scope")
			case "client_version":
				f.exec(t, `UPDATE download_client SET updated_at=clock_timestamp() WHERE id=$1`, f.client)
			case "client_digest":
				f.exec(t, `UPDATE download_client SET username='changed-without-timestamp' WHERE id=$1`, f.client)
			case "mapping":
				f.exec(t, `UPDATE download_client SET remote_path_map='{"/remote":"/changed"}' WHERE id=$1`, f.client)
			case "invalid_mapping":
				f.exec(t, `UPDATE download_client SET remote_path_map='{"/remote":"/scope-source","bad":"/bad"}' WHERE id=$1`, f.client)
			case "root_path":
				f.exec(t, `UPDATE library_root SET path='/changed-target' WHERE id=$1`, f.root)
			case "generation":
				f.exec(t, `UPDATE library_root SET generation=generation+1 WHERE id=$1`, f.root)
			case "marker":
				f.exec(t, `UPDATE library_root SET marker_filename='.changed-marker' WHERE id=$1`, f.root)
			case "offline":
				f.exec(t, `UPDATE library_root SET health='offline' WHERE id=$1`, f.root)
			case "baseline":
				f.exec(t, `UPDATE library_root SET baseline_completed=false WHERE id=$1`, f.root)
			}
			before := discoveryBusinessFacts(t, f)
			if err := f.discovery.ValidateUntrackedDiscovery(t.Context(), c); err == nil {
				t.Fatal("changed authority validated")
			}
			if err := f.discovery.RecordUntrackedDiscovery(t.Context(), c, discoverySample()); err == nil {
				t.Fatal("changed authority wrote observations")
			}
			if err := f.discovery.CompleteUntrackedDiscovery(t.Context(), c); err == nil {
				t.Fatal("changed authority completed job")
			}
			f.count(t, `SELECT count(*) FROM untracked_discovered_file`, 0)
			f.count(t, `SELECT count(*) FROM job WHERE kind='untracked_discovery' AND state='running'`, 1)
			if discoveryBusinessFacts(t, f) != before {
				t.Fatal("rejected operation changed protected graph")
			}
			page, err := f.discovery.ListUntrackedDiscoveryScopes(t.Context(), "", 64)
			want := 0
			if kind == "scope_version" {
				want = 1
			}
			if err != nil || len(page.Scopes) != want {
				t.Fatal("source list did not reflect current authority", page, err)
			}
		})
	}
}

func TestUntrackedDiscoveryOriginalReceiptAndExplicitSourceBaselinePG(t *testing.T) {
	f := newUntrackedDiscoveryFixture(t)
	// No library_root exists for /scope-source. Source proof is exactly scope102.
	f.count(t, `SELECT count(*) FROM library_root WHERE path='/scope-source'`, 0)
	page, err := f.discovery.ListUntrackedDiscoveryScopes(t.Context(), "", 64)
	if err != nil || len(page.Scopes) != 1 || page.Scopes[0].SourceIdentity.Inode != 222 {
		t.Fatal(page, err)
	}
	f.request(t)
	c := f.claim(t)
	if c.UntrackedDiscoverySnapshot().Scope.SourceIdentity.Inode != 222 {
		t.Fatal(c.UntrackedDiscoverySnapshot())
	}
	// Corrupt an immutable receipt only within a rollback-owned transaction is
	// unnecessary: substituting an invalid current JSON is rejected by schema102.
	_, err = f.p.Exec(t.Context(), `UPDATE untracked_directory_scope SET snapshot=jsonb_set(snapshot,'{source_identity,Inode}','999'::jsonb) WHERE id=$1`, f.scope)
	if err == nil {
		t.Fatal("schema102 receipt protection weakened")
	}
	if err = f.discovery.ValidateUntrackedDiscovery(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	later := uuid.New()
	f.exec(t, `INSERT INTO scan_run(id,root_id,scan_token,generation,start_mutation_seq,state,finished_at) SELECT $1,id,scan_token,generation,mutation_seq,'succeeded',clock_timestamp() FROM library_root WHERE id=$2`, later, f.root)
	f.exec(t, `INSERT INTO scan_finalization(scan_run_id,root_id,expected_path,expected_media_type,expected_marker_filename,expected_health_revision,root_device,root_inode,exhausted,completed_at) SELECT $1,id,path,media_type,marker_filename,health_revision,9,999,true,clock_timestamp() FROM library_root WHERE id=$2`, later, f.root)
	if err = f.discovery.ValidateUntrackedDiscovery(t.Context(), c); err != nil {
		t.Fatal("later scan rebound earliest baseline", err)
	}
}

func TestUntrackedDiscoveryReadAuthorityAndStablePaginationPG(t *testing.T) {
	f := newUntrackedDiscoveryFixture(t)
	f.request(t)
	c := f.claim(t)
	samples := discoverySample()
	for i := range 5 {
		sample := samples[0]
		sample.RelativePath = fmt.Sprintf("external/%d.mkv", i)
		sample.Identity.Inode += int64(i)
		if err := f.discovery.RecordUntrackedDiscovery(t.Context(), c, []ports.UntrackedFileObservation{sample}); err != nil {
			t.Fatal(err)
		}
	}
	for _, actor := range []*ports.Actor{nil, f.user(t), {Type: ports.ActorTypeUser, ID: f.a.ID, Role: "member"}, {Type: ports.ActorTypeUser, ID: f.a.ID, Role: "admin", Scopes: []string{}}, {Type: ports.ActorTypeSystem, ID: f.a.ID, Role: "admin"}} {
		page, err := f.discovery.ReadUntrackedDiscovery(t.Context(), actor, f.scope, "", 2)
		if err == nil || len(page.Files) != 0 {
			t.Fatal("unauthorized discovery exposed", page, err)
		}
	}
	first, err := f.discovery.ReadUntrackedDiscovery(t.Context(), f.a, f.scope, "", 2)
	if err != nil || len(first.Files) != 2 || first.Complete || first.NextCursor == "" {
		t.Fatal(first, err)
	}
	if err = f.discovery.CompleteUntrackedDiscovery(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	f.request(t)
	c = f.claim(t)
	// Update a row behind the cursor and one ahead, and create a new row. Updates
	// must not skip existing rows; creation after the frozen horizon is excluded.
	for _, file := range []ports.UntrackedDiscoveredFile{first.Files[0], f.files(t)[4]} {
		sample := ports.UntrackedFileObservation{RelativePath: file.RelativePath, Identity: file.Identity}
		sample.Identity.Size++
		if err = f.discovery.RecordUntrackedDiscovery(t.Context(), c, []ports.UntrackedFileObservation{sample}); err != nil {
			t.Fatal(err)
		}
	}
	newSample := discoverySample()
	newSample[0].RelativePath = "external/after-horizon.mkv"
	if err = f.discovery.RecordUntrackedDiscovery(t.Context(), c, newSample); err != nil {
		t.Fatal(err)
	}
	seen := map[uuid.UUID]bool{}
	for _, file := range first.Files {
		seen[file.ID] = true
	}
	cursor := first.NextCursor
	for range 4 {
		page, e := f.discovery.ReadUntrackedDiscovery(t.Context(), f.a, f.scope, cursor, 2)
		if e != nil {
			t.Fatal(e)
		}
		for _, file := range page.Files {
			if seen[file.ID] || file.RelativePath == newSample[0].RelativePath {
				t.Fatal("duplicate or horizon escaped", file)
			}
			seen[file.ID] = true
		}
		if page.Complete {
			cursor = ""
			break
		}
		cursor = page.NextCursor
	}
	if len(seen) != 5 || cursor != "" {
		t.Fatal("pagination lost existing observations", len(seen), cursor)
	}
	for _, bad := range []string{"garbage", first.NextCursor + "x", strings.Replace(first.NextCursor, ".", "..", 1)} {
		_, e := f.discovery.ReadUntrackedDiscovery(t.Context(), f.a, f.scope, bad, 2)
		checkinErr(t, e, ports.ErrRejected)
	}
	_, err = f.discovery.ListUntrackedDiscoveryScopes(t.Context(), first.NextCursor, 2)
	checkinErr(t, err, ports.ErrRejected)
	// A new version may read old observations, but cannot reuse the old cursor.
	in := f.in
	in.ExpectedVersion = 1
	f.save(t, in, "next-version")
	files := f.files(t)
	if files[0].ScopeVersion != 1 {
		t.Fatal("history rewritten as current inventory", files)
	}
	_, err = f.discovery.ReadUntrackedDiscovery(t.Context(), f.a, f.scope, first.NextCursor, 2)
	checkinErr(t, err, ports.ErrRejected)
}

func TestUntrackedDiscoveryDeferredBatchRollbackPG(t *testing.T) {
	for _, kind := range []string{"client", "role", "root", "lease", "state", "projection"} {
		t.Run(kind, func(t *testing.T) {
			f := newUntrackedDiscoveryFixture(t)
			f.request(t)
			c := f.claim(t)
			var mutation string
			switch kind {
			case "client":
				mutation = `UPDATE download_client SET username='tail' WHERE id='` + f.client.String() + `';`
			case "role":
				mutation = `UPDATE app_user SET role='member' WHERE id='` + f.a.ID + `';`
			case "root":
				mutation = `UPDATE library_root SET health='offline' WHERE id='` + f.root.String() + `';`
			case "lease":
				mutation = `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=NEW.last_job_id;`
			case "state":
				mutation = `UPDATE untracked_discovery_state SET requested_sequence=requested_sequence+1 WHERE scope_id=NEW.scope_id;`
			case "projection":
				mutation = `DELETE FROM untracked_discovered_file WHERE id=NEW.id;`
			}
			f.exec(t, `CREATE FUNCTION discovery_batch_tail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+mutation+` RETURN NULL; END $$; CREATE CONSTRAINT TRIGGER discovery_batch_tail AFTER INSERT ON untracked_discovered_file DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION discovery_batch_tail()`)
			before := discoveryBusinessFacts(t, f)
			if err := f.discovery.RecordUntrackedDiscovery(t.Context(), c, discoverySample()); err == nil {
				t.Fatal("deferred mutation committed")
			}
			f.count(t, `SELECT count(*) FROM untracked_discovered_file`, 0)
			f.count(t, `SELECT count(*) FROM untracked_discovery_state WHERE requested_sequence=1`, 1)
			if discoveryBusinessFacts(t, f) != before {
				t.Fatal("tail rollback changed graph")
			}
			if err := f.discovery.ValidateUntrackedDiscovery(t.Context(), c); err != nil {
				t.Fatal("tail mutation escaped rollback", err)
			}
		})
	}
}

func TestUntrackedDiscoveryDeferredCompletionAndQueueRollbackPG(t *testing.T) {
	for _, kind := range []string{"lease", "role", "successor", "state"} {
		t.Run(kind, func(t *testing.T) {
			f := newUntrackedDiscoveryFixture(t)
			f.request(t)
			c := f.claim(t)
			f.request(t)
			mutation := `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=NEW.id;`
			if kind == "role" {
				mutation = `UPDATE app_user SET role='member' WHERE id='` + f.a.ID + `';`
			}
			if kind == "successor" {
				mutation = `UPDATE job SET max_attempts=4 WHERE kind='untracked_discovery' AND state='queued';`
			}
			if kind == "state" {
				mutation = `UPDATE untracked_discovery_state SET requested_sequence=requested_sequence+1 WHERE scope_id='` + f.scope.String() + `';`
			}
			f.exec(t, `CREATE FUNCTION discovery_complete_tail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+mutation+` RETURN NULL; END $$; CREATE CONSTRAINT TRIGGER discovery_complete_tail AFTER UPDATE ON job DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.state='succeeded' AND OLD.state='running') EXECUTE FUNCTION discovery_complete_tail()`)
			if err := f.discovery.CompleteUntrackedDiscovery(t.Context(), c); err == nil {
				t.Fatal("terminal original lease/tail mutation accepted")
			}
			f.count(t, `SELECT count(*) FROM job WHERE kind='untracked_discovery'`, 1)
			f.count(t, `SELECT count(*) FROM job WHERE kind='untracked_discovery' AND state='running'`, 1)
			f.count(t, `SELECT count(*) FROM untracked_discovery_state WHERE completed_sequence=0 AND requested_sequence=2 AND active_job_id IS NOT NULL`, 1)
		})
	}
	t.Run("queue", func(t *testing.T) {
		f := newUntrackedDiscoveryFixture(t)
		f.exec(t, `CREATE FUNCTION discovery_queue_tail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN UPDATE download_client SET username='tail' WHERE id='`+f.client.String()+`'; RETURN NULL; END $$; CREATE CONSTRAINT TRIGGER discovery_queue_tail AFTER INSERT ON job DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.kind='untracked_discovery') EXECUTE FUNCTION discovery_queue_tail()`)
		if _, err := f.discovery.RequestUntrackedDiscovery(t.Context(), f.scope, 1); err == nil {
			t.Fatal("queue tail committed")
		}
		f.count(t, `SELECT count(*) FROM job WHERE kind='untracked_discovery'`, 0)
		f.count(t, `SELECT count(*) FROM untracked_discovery_state`, 0)
	})
}

func TestUntrackedDiscoveryLeasePayloadAndOwnerCannotBeSubstitutedPG(t *testing.T) {
	f := newUntrackedDiscoveryFixture(t)
	f.request(t)
	c := f.claim(t)
	lease := c.UntrackedDiscoverySnapshot().Lease
	wrongToken := lease.Token
	wrongToken++
	for _, wrong := range []ports.JobLease{{ID: lease.ID, Owner: "other", Token: lease.Token}, {ID: lease.ID, Owner: lease.Owner, Token: wrongToken}, {ID: uuid.New(), Owner: lease.Owner, Token: lease.Token}} {
		if capture, err := f.discovery.LoadUntrackedDiscovery(t.Context(), wrong); err == nil || capture != nil {
			t.Fatal(capture, err)
		}
	}
	f.exec(t, `UPDATE job SET payload=payload||'{"extra":true}'::jsonb WHERE id=$1`, lease.ID)
	checkinErr(t, f.discovery.RecordUntrackedDiscovery(t.Context(), c, discoverySample()), ports.ErrUntrackedDiscoveryStale)
	if capture, err := f.discovery.LoadUntrackedDiscovery(t.Context(), lease); err == nil || capture != nil {
		t.Fatal(capture, err)
	}
	f.count(t, `SELECT count(*) FROM untracked_discovered_file`, 0)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := f.discovery.ReadUntrackedDiscovery(ctx, f.a, f.scope, "", 64)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestUntrackedDiscoveryScopeVersionResetsObservationPG(t *testing.T) {
	f := newUntrackedDiscoveryFixture(t)
	f.request(t)
	c := f.claim(t)
	if err := f.discovery.RecordUntrackedDiscovery(t.Context(), c, discoverySample()); err != nil {
		t.Fatal(err)
	}
	before := f.files(t)[0]
	if err := f.discovery.CompleteUntrackedDiscovery(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	in := f.in
	in.ExpectedVersion = 1
	f.save(t, in, "version-two")
	if r, err := f.discovery.RequestUntrackedDiscovery(t.Context(), f.scope, 2); err != nil || !r.Queued {
		t.Fatal(r, err)
	}
	c = f.claim(t)
	if err := f.discovery.RecordUntrackedDiscovery(t.Context(), c, discoverySample()); err != nil {
		t.Fatal(err)
	}
	after := f.files(t)[0]
	if after.ScopeVersion != 2 || after.Revision != before.Revision+1 || after.Observations != 1 || after.ID != before.ID || !after.FirstObservedAt.After(before.FirstObservedAt) {
		t.Fatal(before, after)
	}
	// LastSeen changes by themselves are telemetry, not capture authority.
	f.exec(t, `UPDATE untracked_discovered_file SET last_observed_at=clock_timestamp() WHERE scope_id=$1`, f.scope)
	if err := f.discovery.ValidateUntrackedDiscovery(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(before, after) {
		t.Fatal("scope transition was invisible")
	}
	if _, err := db.New(f.p).GetJobByID(t.Context(), c.UntrackedDiscoverySnapshot().Lease.ID); err != nil {
		t.Fatal(err)
	}
}
