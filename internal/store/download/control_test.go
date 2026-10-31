package download

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	downloadcase "github.com/dalamudx/PulseMedia/internal/usecase/download"
	"github.com/google/uuid"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type nativeControlProbe struct {
	ports.DownloadClient
	info         ports.TorrentInfo
	paused       atomic.Bool
	reads, sends atomic.Int32
	read         func(context.Context) error
	send         func(context.Context) error
}

func (c *nativeControlProbe) ReadTorrentPaused(ctx context.Context, _ string) (*ports.DownloadPauseObservation, error) {
	c.reads.Add(1)
	if c.read != nil {
		if e := c.read(ctx); e != nil {
			return nil, e
		}
	}
	info := c.info
	return &ports.DownloadPauseObservation{Info: &info, Paused: c.paused.Load()}, nil
}
func (c *nativeControlProbe) SetTorrentPaused(ctx context.Context, _ string, paused bool) error {
	c.sends.Add(1)
	c.paused.Store(paused)
	if c.send != nil {
		return c.send(ctx)
	}
	return nil
}

type controlFixture struct {
	*ratePublisherFixture
	owner      *ports.Actor
	native     *nativeControlProbe
	service    *downloadcase.ControlService
	root, unit uuid.UUID
}

func newControlFixture(t *testing.T) *controlFixture {
	t.Helper()
	base := newRatePublisherFixture(t)
	owner := uuid.New()
	root, media, unit, job := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	ratePublisherExec(t, base.pool, `INSERT INTO app_user(id,username,password_hash,role)VALUES($1,'control-owner','unused','member')`, owner)
	ratePublisherExec(t, base.pool, `INSERT INTO library_root(id,path,media_type,health,name)VALUES($1,'/control-library','movie','online','control root')`, root)
	ratePublisherExec(t, base.pool, `INSERT INTO media(id,title,media_type)VALUES($1,'Control fixture','movie')`, media)
	ratePublisherExec(t, base.pool, `INSERT INTO media_unit(id,media_id,unit_key)VALUES($1,$2,'movie')`, unit, media)
	ratePublisherExec(t, base.pool, `INSERT INTO download_requirement(id,operation_id,unit_id,root_id,actor_type,actor_id)VALUES($1,$2,$3,$4,'user',$5)`, uuid.New(), base.task.OperationID, unit, root, owner.String())
	ratePublisherExec(t, base.pool, `UPDATE download_task SET state='submitting' WHERE id=$1`, base.task.ID)
	ratePublisherExec(t, base.pool, `INSERT INTO job(id,kind,state,payload,attempts,lease_owner,lease_token,lease_until)VALUES($1,'download_submission','running',jsonb_build_object('operation_id',$2::text,'candidate_id',$3::text),1,'control-fixture',1,clock_timestamp()+interval '60 seconds')`, job, base.task.OperationID, base.task.CandidateID)
	q := db.New(base.pool)
	bound, e := q.GetBoundSubmissionClient(t.Context(), base.task.ClientID)
	if e != nil {
		t.Fatal(e)
	}
	if e = q.CreateDownloadTaskClientBinding(t.Context(), db.CreateDownloadTaskClientBindingParams{TaskID: base.task.ID, ClientID: base.task.ClientID, ClientStamp: bound.ConfigStamp, JobID: job, LeaseOwner: "control-fixture", LeaseToken: 1}); e != nil {
		t.Fatal(e)
	}
	ratePublisherExec(t, base.pool, `UPDATE download_task SET state='tracking' WHERE id=$1`, base.task.ID)
	native := &nativeControlProbe{DownloadClient: base.client, info: base.info}
	native.info.Tags = "pulsemedia,pulsemedia:op:" + base.task.OperationID.String()
	base.repo.RegisterClient(base.task.ClientID, native)
	return &controlFixture{ratePublisherFixture: base, owner: &ports.Actor{Type: ports.ActorTypeUser, ID: owner.String(), Role: ports.RoleMember}, native: native, service: downloadcase.NewControlService(base.repo), root: root, unit: unit}
}
func (f *controlFixture) apply(ctx context.Context, key, action string, shared bool) (*ports.DownloadControlReceipt, error) {
	return f.service.Apply(ctx, f.owner, f.task.OperationID, key, ports.DownloadControlCommand{Action: action, Confirmed: true, AffectShared: shared})
}
func (f *controlFixture) count(t *testing.T, table string) int {
	t.Helper()
	var count int
	if e := f.pool.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&count); e != nil {
		t.Fatal(e)
	}
	return count
}
func TestDownloadControlPersistentReceiptAndProtectionPG(t *testing.T) {
	f := newControlFixture(t)
	before := f.protected(t)
	task := f.taskJSON(t)
	got, e := f.apply(t.Context(), "pause-one", "pause", false)
	if e != nil || got.State != "confirmed" {
		t.Fatal(got, e)
	}
	replay, e := f.apply(t.Context(), "pause-one", "pause", false)
	if e != nil || *replay != *got || f.native.sends.Load() != 1 || f.native.reads.Load() != 1 {
		t.Fatal("replay sent", replay, e)
	}
	receipt, e := f.service.Get(t.Context(), f.owner, f.task.OperationID, "pause-one")
	if e != nil || *receipt != *got || f.native.reads.Load() != 1 {
		t.Fatal("GET touched native", e)
	}
	if _, e = f.apply(t.Context(), "pause-one", "resume", false); !errors.Is(e, ports.ErrDownloadControlConflict) {
		t.Fatal("key rebound", e)
	}
	resumed, e := f.apply(t.Context(), "resume-two", "resume", false)
	if e != nil || resumed.State != "confirmed" || f.native.sends.Load() != 2 {
		t.Fatal(resumed, e)
	}
	if !reflect.DeepEqual(before, f.protected(t)) || task != f.taskJSON(t) {
		t.Fatal("control changed task, jobs, requirements or reservations")
	}
	for _, table := range []string{"download_control_command", "download_control_return", "download_control_confirmation"} {
		if f.count(t, table) != 2 {
			t.Fatal(table)
		}
	}
	if _, e = f.pool.Exec(t.Context(), `UPDATE download_control_command SET action='resume'`); e == nil {
		t.Fatal("permit mutable")
	}
}
func TestDownloadControlSharedAndCurrentRolesPG(t *testing.T) {
	f := newControlFixture(t)
	other := uuid.New()
	ratePublisherExec(t, f.pool, `INSERT INTO download_requirement(id,operation_id,unit_id,root_id,actor_type,actor_id)VALUES($1,$2,$3,$4,'user',$5)`, uuid.New(), f.task.OperationID, f.unit, f.root, other.String())
	for _, shared := range []bool{false, true} {
		if _, e := f.apply(t.Context(), uuid.NewString(), "pause", shared); !errors.Is(e, ports.ErrForbidden) {
			t.Fatal("member controlled shared", e)
		}
	}
	f.owner.Role = ports.RoleAdmin
	if _, e := f.apply(t.Context(), "spoofed-admin", "pause", true); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("actor role trusted", e)
	}
	ratePublisherExec(t, f.pool, `UPDATE app_user SET role='admin' WHERE id=$1`, uuid.MustParse(f.owner.ID))
	if _, e := f.apply(t.Context(), "admin-no-ack", "pause", false); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("implicit admin sharing", e)
	}
	if got, e := f.apply(t.Context(), "admin-ack", "pause", true); e != nil || got.State != "confirmed" {
		t.Fatal(got, e)
	}
	ratePublisherExec(t, f.pool, `UPDATE app_user SET is_active=false WHERE id=$1`, uuid.MustParse(f.owner.ID))
	if _, e := f.service.Get(t.Context(), f.owner, f.task.OperationID, "admin-ack"); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("disabled replay", e)
	}
	if f.native.sends.Load() != 1 {
		t.Fatal("authorization caused send")
	}
}
func TestDownloadControlUnknownOnlyObservesPG(t *testing.T) {
	f := newControlFixture(t)
	f.native.send = func(context.Context) error { return ports.ErrTimeout }
	got, e := f.apply(t.Context(), "lost-ack", "pause", false)
	if e != nil || got.State != "unknown" {
		t.Fatal(got, e)
	}
	if _, e = f.apply(t.Context(), "replacement-key", "pause", false); !errors.Is(e, ports.ErrDownloadControlConflict) {
		t.Fatal("unknown replaced", e)
	}
	f.native.paused.Store(false)
	got, e = f.service.Observe(t.Context(), f.owner, f.task.OperationID, "lost-ack")
	if e != nil || got.State != "unknown" {
		t.Fatal(got, e)
	}
	f.native.paused.Store(true)
	got, e = f.service.Observe(t.Context(), f.owner, f.task.OperationID, "lost-ack")
	if e != nil || got.State != "confirmed" || f.native.sends.Load() != 1 {
		t.Fatal("observation resent", got, e)
	}
	if f.count(t, "download_control_command") != 1 || f.count(t, "download_control_confirmation") != 1 {
		t.Fatal("permit or outcome duplicated")
	}
}
func TestDownloadControlCancellationRetainsPhysicalGatePG(t *testing.T) {
	f := newControlFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.native.send = func(context.Context) error { close(entered); <-release; return nil }
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, e := f.apply(ctx, "blocked-call", "pause", false); done <- e }()
	drain := func() {
		once.Do(func() {
			close(release)
			if e := <-done; e != nil {
				t.Errorf("actual return writeback: %v", e)
			}
		})
	}
	defer drain()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("not dispatched")
	}
	cancel()
	got, e := f.service.Get(t.Context(), f.owner, f.task.OperationID, "blocked-call")
	if e != nil || got.State != "unknown" {
		t.Fatal(got, e)
	}
	second := NewRepository(f.pool, idgen.New(), make([]byte, 32))
	second.RegisterClient(f.task.ClientID, f.native)
	service := downloadcase.NewControlService(second)
	if _, e = service.Apply(t.Context(), f.owner, f.task.OperationID, "new-key", ports.DownloadControlCommand{Action: "resume", Confirmed: true}); !errors.Is(e, ports.ErrDownloadControlConflict) {
		t.Fatal("cancellation released gate", e)
	}
	if _, e = service.Observe(t.Context(), f.owner, f.task.OperationID, "blocked-call"); !errors.Is(e, ports.ErrDownloadControlConflict) {
		t.Fatal("observed active physical call", e)
	}
	select {
	case <-done:
		t.Fatal("returned before physical call")
	default:
	}
	drain()
	got, e = f.service.Get(t.Context(), f.owner, f.task.OperationID, "blocked-call")
	if e != nil || got.State != "confirmed" {
		t.Fatal("independent writeback", got, e)
	}
}
func TestDownloadControlLostDatabaseDoesNotAuthorizeReplacementPG(t *testing.T) {
	f := newControlFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.native.send = func(context.Context) error { close(entered); <-release; return nil }
	done := make(chan error, 1)
	go func() { _, e := f.apply(t.Context(), "lost-session", "pause", false); done <- e }()
	drain := func() {
		once.Do(func() {
			close(release)
			if e := <-done; e == nil {
				t.Error("lost session published success")
			}
		})
	}
	defer drain()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("not dispatched")
	}
	ratePublisherExec(t, f.pool, `SELECT pg_terminate_backend(backend_pid) FROM download_control_command WHERE request_key='lost-session'`)
	if _, e := f.apply(t.Context(), "new-after-disconnect", "resume", false); !errors.Is(e, ports.ErrDownloadControlConflict) {
		t.Fatal("lost DB released permit", e)
	}
	got, e := f.service.Observe(t.Context(), f.owner, f.task.OperationID, "lost-session")
	if e != nil || got.State != "unknown" || f.native.reads.Load() != 1 {
		t.Fatal("lost connection counted physical return", got, e)
	}
	drain()
	if f.count(t, "download_control_return") != 0 || f.native.sends.Load() != 1 {
		t.Fatal("forged return or repeated send")
	}
}
func TestDownloadControlRechecksBeforeSendPG(t *testing.T) {
	for _, change := range []string{"role", "shared", "endpoint", "hash", "tag", "upgrade", "other-task"} {
		t.Run(change, func(t *testing.T) {
			f := newControlFixture(t)
			f.native.read = func(context.Context) error {
				switch change {
				case "role":
					ratePublisherExec(t, f.pool, `UPDATE app_user SET is_active=false WHERE id=$1`, uuid.MustParse(f.owner.ID))
				case "shared":
					ratePublisherExec(t, f.pool, `INSERT INTO download_requirement(id,operation_id,unit_id,root_id,actor_type,actor_id)VALUES($1,$2,$3,$4,'user',$5)`, uuid.New(), f.task.OperationID, f.unit, f.root, uuid.NewString())
				case "endpoint":
					ratePublisherExec(t, f.pool, `UPDATE download_client SET url='https://changed.invalid' WHERE id=$1`, f.task.ClientID)
				case "hash":
					f.native.info.InfoHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
				case "tag":
					f.native.info.Tags = "pulsemedia:op:" + uuid.NewString()
				case "other-task":
					ratePublisherExec(t, f.pool, `INSERT INTO download_task(id,operation_id,client_id,candidate_id,info_hash,state,save_path)VALUES($1,$2,$3,$4,$5,'tracking','/downloads')`, uuid.New(), uuid.New(), f.task.ClientID, f.task.CandidateID, f.task.InfoHash)
				case "upgrade":
					ratePublisherExec(t, f.pool, `UPDATE download_requirement SET submission_fence='{"goal":"upgrade"}' WHERE operation_id=$1`, f.task.OperationID)
				}
				return nil
			}
			if _, e := f.apply(t.Context(), "changed-before-send", "pause", false); e == nil {
				t.Fatal("stale command accepted")
			}
			if f.native.sends.Load() != 0 || f.count(t, "download_control_command") != 0 {
				t.Fatal("authority changed but send permit committed")
			}
		})
	}
}
func TestDownloadControlConcurrentOriginalKeyPG(t *testing.T) {
	f := newControlFixture(t)
	start := make(chan struct{})
	done := make(chan error, 8)
	for range 8 {
		go func() { <-start; _, e := f.apply(t.Context(), "one-concurrent-key", "pause", false); done <- e }()
	}
	close(start)
	successes := 0
	for range 8 {
		e := <-done
		if e == nil {
			successes++
		} else if !errors.Is(e, ports.ErrDownloadControlConflict) {
			t.Fatal(e)
		}
	}
	if successes == 0 || f.native.sends.Load() != 1 || f.count(t, "download_control_command") != 1 {
		t.Fatal("original key duplicated native send", successes, f.native.sends.Load())
	}
}

func TestDownloadControlExpiredPhysicalReturnNeedsObservationPG(t *testing.T) {
	f := newControlFixture(t)
	ratePublisherExec(t, f.pool, `CREATE FUNCTION shorten_control_deadline() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN NEW.expires_at=clock_timestamp()+interval '250 milliseconds'; RETURN NEW; END $$`)
	ratePublisherExec(t, f.pool, `CREATE TRIGGER control_deadline BEFORE INSERT ON download_control_command FOR EACH ROW EXECUTE FUNCTION shorten_control_deadline()`)
	f.native.send = func(context.Context) error { ratePublisherExec(t, f.pool, `SELECT pg_sleep(0.35)`); return nil }
	got, e := f.apply(t.Context(), "expired-call", "pause", false)
	if e != nil || got.State != "unknown" {
		t.Fatal("expired call published success", got, e)
	}
	if f.count(t, "download_control_return") != 1 || f.count(t, "download_control_confirmation") != 0 {
		t.Fatal("physical return lost or stale success")
	}
	got, e = f.service.Observe(t.Context(), f.owner, f.task.OperationID, "expired-call")
	if e != nil || got.State != "confirmed" || f.native.sends.Load() != 1 {
		t.Fatal("expired command replayed", got, e)
	}
}
func TestDownloadControlRevokedDuringSendRecordsOnlyPhysicalReturnPG(t *testing.T) {
	f := newControlFixture(t)
	f.native.send = func(context.Context) error {
		ratePublisherExec(t, f.pool, `UPDATE app_user SET is_active=false WHERE id=$1`, uuid.MustParse(f.owner.ID))
		return nil
	}
	if _, e := f.apply(t.Context(), "revoked-call", "pause", false); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("revoked callback published success", e)
	}
	if f.count(t, "download_control_return") != 1 || f.count(t, "download_control_confirmation") != 0 {
		t.Fatal("revocation released physical evidence")
	}
	ratePublisherExec(t, f.pool, `UPDATE app_user SET is_active=true WHERE id=$1`, uuid.MustParse(f.owner.ID))
	got, e := f.service.Observe(t.Context(), f.owner, f.task.OperationID, "revoked-call")
	if e != nil || got.State != "confirmed" || f.native.sends.Load() != 1 {
		t.Fatal(got, e)
	}
}
func TestDownloadControlRejectsRewrittenPermitPG(t *testing.T) {
	f := newControlFixture(t)
	ratePublisherExec(t, f.pool, `CREATE FUNCTION rewrite_control_action() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN NEW.action='resume'; RETURN NEW; END $$`)
	ratePublisherExec(t, f.pool, `CREATE TRIGGER rewrite_control_action BEFORE INSERT ON download_control_command FOR EACH ROW EXECUTE FUNCTION rewrite_control_action()`)
	if _, e := f.apply(t.Context(), "rewritten-call", "pause", false); !errors.Is(e, ports.ErrDownloadControlConflict) {
		t.Fatal("rewritten action accepted", e)
	}
	if f.count(t, "download_control_command") != 0 || f.native.sends.Load() != 0 {
		t.Fatal("rewritten permit committed")
	}
}

func TestDownloadControlAuditFailureRollsBackPermitPG(t *testing.T) {
	f := newControlFixture(t)
	ratePublisherExec(t, f.pool, `CREATE FUNCTION reject_control_audit() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='download.control.authorized' THEN RAISE EXCEPTION 'fixture'; END IF; RETURN NEW; END $$`)
	ratePublisherExec(t, f.pool, `CREATE TRIGGER reject_control_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_control_audit()`)
	if _, e := f.apply(t.Context(), "audit-failure", "pause", false); e == nil {
		t.Fatal("audit failure ignored")
	}
	if f.count(t, "download_control_command") != 0 || f.native.sends.Load() != 0 {
		t.Fatal("partial authorization")
	}
}
