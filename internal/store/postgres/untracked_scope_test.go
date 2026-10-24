package postgres_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

type untrackedFixture struct {
	*checkinFixture
	store               *postgres.UntrackedScopeStore
	scope, root, client uuid.UUID
	in                  ports.UntrackedScopeInput
}

func newUntrackedFixture(t *testing.T, through ...int64) *untrackedFixture {
	t.Helper()
	expectedVersion := int64(postgres.CurrentMigrationVersion)
	if len(through) > 0 {
		expectedVersion = through[0]
	}
	f := &untrackedFixture{checkinFixture: newCheckinFixtureAt(t, repositoryMigrationsThrough(t, through...), expectedVersion), scope: uuid.New(), root: uuid.New(), client: uuid.New()}
	f.exec(t, `INSERT INTO library_root(id,path,name,media_type,health,baseline_completed) VALUES($1,'/scope-target','scope','movie','online',true)`, f.root)
	f.exec(t, `INSERT INTO download_client(id,name,client_type,url,username,password_encrypted,remote_path_map) VALUES($1,'scope','qbittorrent','https://scope.invalid','fixture','fixture','{"/remote":"/scope-source","/remote/nested":"/scope-nested"}')`, f.client)
	scan := uuid.New()
	f.exec(t, `INSERT INTO scan_run(id,root_id,scan_token,generation,start_mutation_seq,state,finished_at) SELECT $1,id,scan_token,generation,mutation_seq,'succeeded',clock_timestamp() FROM library_root WHERE id=$2`, scan, f.root)
	f.exec(t, `INSERT INTO scan_finalization(scan_run_id,root_id,expected_path,expected_media_type,expected_marker_filename,expected_health_revision,root_device,root_inode,exhausted,completed_at) SELECT $1,id,path,media_type,marker_filename,health_revision,1,111,true,clock_timestamp() FROM library_root WHERE id=$2`, scan, f.root)
	f.in = ports.UntrackedScopeInput{Enabled: true, ClientID: f.client, RemotePrefix: "/remote", TargetRootID: f.root}
	if err := f.p.QueryRow(t.Context(), `SELECT c.updated_at,r.generation FROM download_client c CROSS JOIN library_root r WHERE c.id=$1 AND r.id=$2`, f.client, f.root).Scan(&f.in.ExpectedClientVersion, &f.in.ExpectedRootGeneration); err != nil {
		t.Fatal(err)
	}
	f.store = postgres.NewUntrackedScopeStore(f.p, idgen.New(), hintClock{})
	return f
}

func (f *untrackedFixture) prepare(t *testing.T, in ports.UntrackedScopeInput, key string) ports.UntrackedScopeCapture {
	t.Helper()
	p, err := f.store.PrepareUntrackedScope(t.Context(), f.a, f.scope, in, key)
	if err != nil || p == nil || p.Capture == nil || p.Receipt != nil {
		t.Fatal(p, err)
	}
	return p.Capture
}

func (f *untrackedFixture) save(t *testing.T, in ports.UntrackedScopeInput, key string) *ports.UntrackedScopeReceipt {
	t.Helper()
	c := f.prepare(t, in, key)
	r, err := f.store.CommitUntrackedScope(t.Context(), c, ports.UntrackedScopeEvidence{SourceIdentity: ports.FileIdentity{Device: 2, Inode: 222, Size: 19, Mtime: 20}, TargetIdentity: ports.FileIdentity{Device: 1, Inode: 111, Size: 99, Mtime: 100}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (f *untrackedFixture) guard(t *testing.T, root uuid.UUID, rel string, want error) {
	t.Helper()
	tx, err := f.p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var id uuid.UUID
	if err = tx.QueryRow(t.Context(), `SELECT id FROM library_root WHERE id=$1 FOR UPDATE`, root).Scan(&id); err != nil {
		t.Fatal(err)
	}
	checkinErr(t, postgres.GuardUntrackedMovieRecognition(t.Context(), db.New(tx), root, rel), want)
}

func untrackedFacts(t *testing.T, f *untrackedFixture) string {
	t.Helper()
	return publisherFrozen(t, f.p, `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(x)) FROM unit_fact x),(SELECT jsonb_agg(to_jsonb(x)) FROM wanted_item x),(SELECT jsonb_agg(to_jsonb(x)) FROM job x),(SELECT jsonb_agg(to_jsonb(x)) FROM download_task x),(SELECT jsonb_agg(to_jsonb(x)) FROM library_file_unit x))::text`)
}

func TestUntrackedScopePersistenceAndPermanentGuardPG(t *testing.T) {
	f := newUntrackedFixture(t)
	source := uuid.New()
	f.exec(t, `INSERT INTO library_root(id,path,name,media_type) VALUES($1,'/scope-source','source','movie')`, source)
	before := untrackedFacts(t, f)
	f.guard(t, source, "movie.mkv", nil)
	r := f.save(t, f.in, "first")
	if r.ExecutionAllowed || r.Scope.SourceIdentity != (ports.FileIdentity{Device: 2, Inode: 222}) || r.Scope.TargetIdentity != (ports.FileIdentity{Device: 1, Inode: 111}) || r.Scope.LocalRoot != "/scope-source" {
		t.Fatal(r)
	}
	f.guard(t, source, "movie.mkv", ports.ErrMovieLinkStale)
	f.guard(t, f.root, "movie.mkv", nil)
	f.exec(t, `UPDATE download_client SET remote_path_map='{}',updated_at=clock_timestamp() WHERE id=$1`, f.client)
	f.exec(t, `UPDATE library_root SET health='offline' WHERE id=$1`, f.root)
	in := f.in
	in.Enabled = false
	in.ExpectedVersion = 1
	disabled := f.save(t, in, "disable")
	if disabled.Scope.Enabled || disabled.Scope.Version != 2 || disabled.Scope.SourceIdentity != r.Scope.SourceIdentity {
		t.Fatal(disabled)
	}
	f.guard(t, source, "movie.mkv", ports.ErrMovieLinkStale)
	replay, err := f.store.PrepareUntrackedScope(t.Context(), f.a, f.scope, f.in, "first")
	if err != nil || replay.Capture != nil || !reflect.DeepEqual(replay.Receipt, r) {
		t.Fatal(replay, err)
	}
	if untrackedFacts(t, f) != before {
		t.Fatal("configuration wrote protected business facts")
	}
	for _, sql := range []string{`TRUNCATE untracked_directory_scope_receipt CASCADE`, `DELETE FROM untracked_directory_scope_receipt`, `UPDATE untracked_directory_scope_receipt SET request_hash=repeat('0',64)`, `UPDATE untracked_directory_scope SET local_root='/other'`, `DELETE FROM download_client WHERE id='` + f.client.String() + `'`, `DELETE FROM library_root WHERE id='` + f.root.String() + `'`} {
		if _, err = f.p.Exec(t.Context(), sql); err == nil {
			t.Fatal("history/reference mutation accepted", sql)
		}
	}
}

func TestUntrackedScopeAuthorizationAndReplayPG(t *testing.T) {
	f := newUntrackedFixture(t)
	original := f.save(t, f.in, "first")
	other := f.user(t)
	token := *f.a
	token.Scopes = []string{"*"}
	for _, a := range []*ports.Actor{nil, other, &token} {
		want := ports.ErrForbidden
		if a == nil {
			want = ports.ErrUnauthorized
		}
		r, err := f.store.ReadUntrackedScope(t.Context(), a, f.scope)
		if r != nil {
			t.Fatal("path leaked")
		}
		checkinErr(t, err, want)
		receipt, err := f.store.ReadUntrackedScopeReceipt(t.Context(), a, f.scope, "first")
		if receipt != nil {
			t.Fatal("receipt leaked")
		}
		checkinErr(t, err, want)
	}
	in := f.in
	in.ExpectedClientVersion = in.ExpectedClientVersion.In(time.FixedZone("nonUTC", 9*3600))
	replay, err := f.store.PrepareUntrackedScope(t.Context(), f.a, f.scope, in, "first")
	if err != nil || !reflect.DeepEqual(replay.Receipt, original) || replay.Capture != nil {
		t.Fatal(replay, err)
	}
	in.Enabled = false
	_, err = f.store.PrepareUntrackedScope(t.Context(), f.a, f.scope, in, "first")
	checkinErr(t, err, ports.ErrEndpointVersionConflict)
	f.exec(t, `UPDATE app_user SET is_active=false WHERE id=$1`, uuid.MustParse(f.a.ID))
	_, err = f.store.ReadUntrackedScopeReceipt(t.Context(), f.a, f.scope, "first")
	checkinErr(t, err, ports.ErrForbidden)
}

func TestUntrackedScopeConcurrentKeyAndVersionPG(t *testing.T) {
	f := newUntrackedFixture(t)
	a := f.prepare(t, f.in, "same")
	b := f.prepare(t, f.in, "same")
	conflict := f.prepare(t, f.in, "different")
	evidence := ports.UntrackedScopeEvidence{SourceIdentity: ports.FileIdentity{Device: 2, Inode: 222}, TargetIdentity: ports.FileIdentity{Device: 1, Inode: 111}}
	var wg sync.WaitGroup
	results := make([]*ports.UntrackedScopeReceipt, 2)
	errs := make([]error, 2)
	for i, c := range []ports.UntrackedScopeCapture{a, b} {
		wg.Add(1)
		go func() { defer wg.Done(); results[i], errs[i] = f.store.CommitUntrackedScope(t.Context(), c, evidence) }()
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || !reflect.DeepEqual(results[0], results[1]) {
		t.Fatal(results, errs)
	}
	_, err := f.store.CommitUntrackedScope(t.Context(), conflict, evidence)
	checkinErr(t, err, ports.ErrEndpointVersionConflict)
	var audits int
	if err = f.p.QueryRow(t.Context(), `SELECT count(*) FROM audit_log WHERE action='untracked.scope.saved'`).Scan(&audits); err != nil || audits != 1 {
		t.Fatal(audits, err)
	}
}

func TestUntrackedScopeCaptureChangeAndTailPG(t *testing.T) {
	for _, kind := range []string{"client", "root", "role", "tail", "mapping", "source", "target"} {
		t.Run(kind, func(t *testing.T) {
			f := newUntrackedFixture(t)
			c := f.prepare(t, f.in, "capture")
			evidence := ports.UntrackedScopeEvidence{SourceIdentity: ports.FileIdentity{Device: 2, Inode: 222}, TargetIdentity: ports.FileIdentity{Device: 1, Inode: 111}}
			switch kind {
			case "client":
				f.exec(t, `UPDATE download_client SET username='changed' WHERE id=$1`, f.client)
			case "root":
				f.exec(t, `UPDATE library_root SET path='/changed' WHERE id=$1`, f.root)
			case "role":
				f.exec(t, `UPDATE app_user SET role='member' WHERE id=$1`, uuid.MustParse(f.a.ID))
			case "mapping":
				f.exec(t, `UPDATE download_client SET remote_path_map='{"/remote":"/scope-source","bad":"/bad"}' WHERE id=$1`, f.client)
			case "source":
				evidence.SourceIdentity.Inode = 0
			case "target":
				evidence.TargetIdentity.Inode = 222
			case "tail":
				f.exec(t, `CREATE FUNCTION untracked_test_tail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN UPDATE download_client SET username='tail' WHERE id=(NEW.payload->>'client_id')::uuid; RETURN NULL; END $$; CREATE CONSTRAINT TRIGGER untracked_test_tail AFTER INSERT ON audit_log DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION untracked_test_tail()`)
			}
			if _, err := f.store.CommitUntrackedScope(t.Context(), c, evidence); err == nil {
				t.Fatal("changed capture committed")
			}
			var n int
			if err := f.p.QueryRow(t.Context(), `SELECT count(*) FROM untracked_directory_scope`).Scan(&n); err != nil || n != 0 {
				t.Fatal(n, err)
			}
		})
	}
}

func TestUntrackedScopeInvalidSelectionPG(t *testing.T) {
	f := newUntrackedFixture(t)
	for _, prefix := range []string{"/remote/file", "/remote/../remote", "relative", "/remote/", ""} {
		in := f.in
		in.RemotePrefix = prefix
		_, err := f.store.PrepareUntrackedScope(t.Context(), f.a, f.scope, in, "invalid")
		if !errors.Is(err, ports.ErrRejected) {
			t.Fatal(prefix, err)
		}
	}
	f.exec(t, `UPDATE download_client SET remote_path_map='{}' WHERE id=$1`, f.client)
	_, err := f.store.PrepareUntrackedScope(t.Context(), f.a, f.scope, f.in, "empty")
	checkinErr(t, err, ports.ErrRejected)
}
