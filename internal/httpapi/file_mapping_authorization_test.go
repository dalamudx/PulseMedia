package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	mediausecase "github.com/dalamudx/PulseMedia/internal/usecase/media"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func exerciseFileMappingAuthority(t *testing.T, a *app.App, file uuid.UUID, command ports.FileMappingCommand) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	p := a.DBPool()
	var user uuid.UUID
	if err := p.QueryRow(ctx, "SELECT id FROM app_user WHERE username='admin'").Scan(&user); err != nil {
		t.Fatal(err)
	}
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: user.String(), Role: ports.RoleAdmin}
	svc := mediausecase.NewFileMappingService(postgres.NewFileMappingStore(p, idgen.New()))
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := p.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	fingerprint := func() []byte {
		t.Helper()
		var b []byte
		err := p.QueryRow(ctx, `SELECT jsonb_build_object(
   'file',(SELECT to_jsonb(f) FROM library_file f WHERE id=$1),
   'root',(SELECT to_jsonb(r) FROM library_root r JOIN library_file f ON f.root_id=r.id WHERE f.id=$1),
   'mappings',(SELECT jsonb_agg(to_jsonb(m) ORDER BY unit_id) FROM library_file_unit m WHERE file_id=$1),
   'facts',(SELECT jsonb_agg(to_jsonb(f) ORDER BY id) FROM unit_fact f WHERE source_file_id=$1),
   'audit',(SELECT count(*) FROM audit_log WHERE action='file.mapping_corrected' AND target_id=$1::text),
   'events',(SELECT count(*) FROM outbox_event WHERE event_type='file.mapping_corrected' AND aggregate_id=$1::text),
   'receipts',(SELECT count(*) FROM idempotency_record WHERE resource_id=$1::text),
   'jobs',(SELECT count(*) FROM job))`, file).Scan(&b)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	before := fingerprint()
	want, err := svc.Correct(ctx, actor, file, command, "mapping-http")
	if err != nil {
		t.Fatal(err)
	}
	wantBytes, _ := json.Marshal(want)
	done := make(chan error, 8)
	for range 8 {
		go func() {
			got, e := svc.Correct(ctx, actor, file, command, "mapping-http")
			if e == nil {
				b, _ := json.Marshal(got)
				if !bytes.Equal(b, wantBytes) {
					e = errors.New("different original correction receipt")
				}
			}
			done <- e
		}()
	}
	for range 8 {
		if e := <-done; e != nil {
			t.Fatal(e)
		}
	}
	if !bytes.Equal(before, fingerprint()) {
		t.Fatal("same-key replay changed file/history/facts or created work")
	}
	for _, state := range []struct {
		name, role string
		active     bool
	}{{"disabled", ports.RoleAdmin, false}, {"downgraded", ports.RoleMember, true}} {
		t.Run(state.name, func(t *testing.T) {
			exec("UPDATE app_user SET role=$2,is_active=$3 WHERE id=$1", user, state.role, state.active)
			defer func() {
				_, _ = p.Exec(context.WithoutCancel(ctx), "UPDATE app_user SET role='admin',is_active=true WHERE id=$1", user)
			}()
			if _, e := svc.Options(ctx, actor, ports.FileMappingOptionsQuery{}); !errors.Is(e, ports.ErrForbidden) {
				t.Fatal("options leaked", e)
			}
			if _, e := svc.Preview(ctx, actor, file, command.MediaID); !errors.Is(e, ports.ErrForbidden) {
				t.Fatal("preview leaked", e)
			}
			if _, e := svc.Correct(ctx, actor, file, command, "mapping-http"); !errors.Is(e, ports.ErrForbidden) {
				t.Fatal("receipt leaked", e)
			}
			if !bytes.Equal(before, fingerprint()) {
				t.Fatal("rejected identity changed mapping or physical protection")
			}
		})
	}
	for _, bad := range []*ports.Actor{
		{Type: ports.ActorTypeUser, ID: user.String(), Role: ports.RoleAdmin, Scopes: []string{"read"}},
		{Type: ports.ActorTypeSystem, ID: user.String(), Role: ports.RoleAdmin},
		{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin},
	} {
		if _, e := svc.Options(ctx, bad, ports.FileMappingOptionsQuery{}); !errors.Is(e, ports.ErrForbidden) {
			t.Fatal("invalid options identity", e)
		}
		if _, e := svc.Preview(ctx, bad, file, command.MediaID); !errors.Is(e, ports.ErrForbidden) {
			t.Fatal("invalid preview identity", e)
		}
		if _, e := svc.Correct(ctx, bad, file, command, "mapping-http"); !errors.Is(e, ports.ErrForbidden) {
			t.Fatal("invalid receipt identity", e)
		}
	}
	hold, e := p.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = hold.Rollback(context.WithoutCancel(ctx)) }()
	if _, e = hold.Exec(ctx, "SELECT id FROM app_user WHERE id=$1 FOR UPDATE", user); e != nil {
		t.Fatal(e)
	}
	waitCtx, stop := context.WithTimeout(ctx, 5*time.Second)
	wait := make(chan error, 1)
	go func() { _, err := svc.Correct(waitCtx, actor, file, command, "mapping-http"); wait <- err }()
	defer stop()
	locked := false
	for !locked && waitCtx.Err() == nil {
		e = p.QueryRow(ctx, `SELECT EXISTS(SELECT FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%FROM app_user%' AND query LIKE '%FOR SHARE%')`).Scan(&locked)
		if e != nil {
			t.Fatal(e)
		}
		if !locked {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if !locked {
		_ = hold.Rollback(ctx)
		stop()
		<-wait
		t.Fatal("did not observe actual user lock")
	}
	rootProbe, e := p.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	_, e = rootProbe.Exec(ctx, "SELECT id FROM library_root WHERE id=(SELECT root_id FROM library_file WHERE id=$1) FOR NO KEY UPDATE NOWAIT", file)
	_ = rootProbe.Rollback(ctx)
	if e != nil {
		_ = hold.Rollback(ctx)
		stop()
		<-wait
		t.Fatal("user waiter already held root", e)
	}
	if _, e = hold.Exec(ctx, "UPDATE app_user SET is_active=false WHERE id=$1", user); e != nil {
		t.Fatal(e)
	}
	if e = hold.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	err = <-wait
	stop()
	exec("UPDATE app_user SET is_active=true WHERE id=$1", user)
	if !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("post-lock disabled account recovered receipt", err)
	}
	if !bytes.Equal(before, fingerprint()) {
		t.Fatal("lock-wait rejection wrote state")
	}
	if _, e = svc.Correct(ctx, actor, file, command, "mapping-http"); e != nil {
		t.Fatal("restored identity cannot recover", e)
	}

	// The original imported facts are unchanged. An additional independent expiring source isolates the final time gate.
	f, e := a.Queries().GetFileByID(ctx, file)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = a.Queries().CreateUnitFact(ctx, db.CreateUnitFactParams{ID: uuid.New(), UnitID: command.UnitIDs[0], Stage: "imported", RootID: pgtype.UUID{Bytes: f.RootID, Valid: true}, SourceFileID: pgtype.UUID{Bytes: file, Valid: true}, QualityProfile: []byte(`{"resolution":"1080p","source":"WEB-DL"}`), ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}}); e != nil {
		t.Fatal(e)
	}
	preview, e := svc.Preview(ctx, actor, file, command.MediaID)
	if e != nil {
		t.Fatal(e)
	}
	change := command
	change.ExpectedStamp = preview.InputStamp
	change.UnitIDs = command.UnitIDs[:1]
	before = fingerprint()
	exec("CREATE FUNCTION public.clock_timestamp() RETURNS timestamptz LANGUAGE sql AS $$SELECT pg_catalog.clock_timestamp()+COALESCE(NULLIF(current_setting('pulse.fixture_clock_shift',true),''),'0 seconds')::interval$$")
	exec("CREATE FUNCTION expire_mapping_receipt() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.idempotency_key='mapping-expiry' THEN PERFORM set_config('search_path','public,pg_catalog',true);PERFORM set_config('pulse.fixture_clock_shift','2 hours',true);END IF;RETURN NEW;END$$")
	exec("CREATE TRIGGER expire_mapping_receipt BEFORE INSERT ON idempotency_record FOR EACH ROW EXECUTE FUNCTION expire_mapping_receipt()")
	_, err = svc.Correct(ctx, actor, file, change, "mapping-expiry")
	exec("DROP TRIGGER expire_mapping_receipt ON idempotency_record")
	exec("DROP FUNCTION expire_mapping_receipt()")
	exec("DROP FUNCTION public.clock_timestamp()")
	if !errors.Is(err, ports.ErrFileMappingConflict) {
		t.Fatal("final receipt accepted expired imported source", err)
	}
	if !bytes.Equal(before, fingerprint()) {
		t.Fatal("expiry retained mapping/root/fact/audit/event/receipt partial writes")
	}
}
