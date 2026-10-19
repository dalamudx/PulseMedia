package postgres_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type automaticTVObservationFixture struct {
	automaticTVSubmissionFixture
	observer *postgres.AutomaticTVObservationStore
	req      ports.AutomaticTVObservationRequest
	conn     *pgxpool.Conn
}

func newAutomaticTVObservationFixture(t *testing.T, record bool) *automaticTVObservationFixture {
	t.Helper()
	f := newAutomaticTVSubmissionFixture(t, false)
	return automaticTVObservationFromSubmission(t, f, record)
}
func automaticTVObservationFromSubmission(t *testing.T, f automaticTVSubmissionFixture, record bool) *automaticTVObservationFixture {
	t.Helper()
	f.prepare(t)
	if version, e := postgres.Migrate(t.Context(), f.pool.Config().ConnString(), automaticTVObservationMigrations(t)); e != nil || version != 90 {
		t.Fatalf("upgrade actual89 fixture to90: %d %v", version, e)
	}
	if record {
		if _, e := f.store.RecordAutomaticTVSubmissionResult(t.Context(), f.result(true, nil)); e != nil {
			t.Fatal(e)
		}
	}
	execIntent(t, f.pool, `UPDATE automatic_tv_physical_attempt SET finished_at=clock_timestamp() WHERE id=$1`, f.command.PhysicalAttemptID)
	digest := sha256.Sum256([]byte("pulsemedia.automatic-tv.physical.v1:" + f.request.OperationID.String()))
	key := int64(binary.BigEndian.Uint64(digest[:8]))
	if _, e := f.physical.Exec(t.Context(), `SELECT pg_advisory_unlock($1)`, key); e != nil {
		t.Fatal(e)
	}
	f.physical.Release()
	f.physical = nil
	if !record {
		execIntent(t, f.pool, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, f.request.Lease.ID)
	}
	return &automaticTVObservationFixture{automaticTVSubmissionFixture: f, observer: postgres.NewAutomaticTVObservationStore(f.pool, idgen.New())}
}
func (f *automaticTVObservationFixture) claim(t *testing.T) {
	t.Helper()
	n, e := f.observer.ScheduleAutomaticTVObservations(t.Context(), 100)
	if e != nil || n != 1 {
		t.Fatalf("schedule %d %v", n, e)
	}
	j, e := db.New(f.pool).ClaimJob(t.Context(), db.ClaimJobParams{Column1: []string{ports.AutomaticTVObservationJobKind}, Column2: "original-observer", Column3: 60000})
	if e != nil {
		t.Fatal(e)
	}
	f.req = ports.AutomaticTVObservationRequest{OperationID: f.request.OperationID, TaskID: f.command.TaskID, Lease: ports.JobLease{ID: j.ID, Owner: "original-observer", Token: j.LeaseToken}}
	f.permit(t)
}
func (f *automaticTVObservationFixture) permit(t *testing.T) {
	t.Helper()
	conn, e := f.pool.Acquire(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	digest := sha256.Sum256([]byte("pulsemedia.automatic-tv.physical.v1:" + f.request.OperationID.String()))
	key := int64(binary.BigEndian.Uint64(digest[:8]))
	var held bool
	if e = conn.QueryRow(t.Context(), `SELECT pg_try_advisory_lock($1)`, key).Scan(&held); e != nil || !held {
		conn.Release()
		t.Fatal("observation session lock", e, held)
	}
	f.conn = conn
	f.req.PhysicalPermitID = uuid.New()
	execIntent(t, f.pool, `INSERT INTO automatic_tv_observation_permit(id,operation_id,task_id,job_id,lease_owner,lease_token,send_attempt_id,backend_pid,advisory_key) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, f.req.PhysicalPermitID, f.req.OperationID, f.req.TaskID, f.req.Lease.ID, f.req.Lease.Owner, f.req.Lease.Token, f.command.PhysicalAttemptID, int32(conn.Conn().PgConn().PID()), key)
	t.Cleanup(func() {
		if f.conn != nil {
			_, _ = f.conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, key)
			f.conn.Release()
			f.conn = nil
		}
	})
}
func (f *automaticTVObservationFixture) finishPermit(t *testing.T) {
	t.Helper()
	execIntent(t, f.pool, `UPDATE automatic_tv_observation_permit SET finished_at=clock_timestamp() WHERE id=$1`, f.req.PhysicalPermitID)
	digest := sha256.Sum256([]byte("pulsemedia.automatic-tv.physical.v1:" + f.request.OperationID.String()))
	key := int64(binary.BigEndian.Uint64(digest[:8]))
	if _, e := f.conn.Exec(t.Context(), `SELECT pg_advisory_unlock($1)`, key); e != nil {
		t.Fatal(e)
	}
	f.conn.Release()
	f.conn = nil
}
func (f *automaticTVObservationFixture) load(t *testing.T) *ports.AutomaticTVObservationSnapshot {
	t.Helper()
	s, e := f.observer.LoadAutomaticTVObservation(t.Context(), f.req)
	if e != nil {
		t.Fatal("load original physical fact", e)
	}
	return s
}
func automaticTVObservationRemote(s *ports.AutomaticTVObservationSnapshot, complete bool) ports.AutomaticTVDownloadObservation {
	native := ports.AutomaticTVRemoteTask{InfoHash: s.InfoHash, SavePath: s.SavePath, ContentPath: s.SavePath + "/" + s.Video.Path, State: "uploading", Tags: []string{"pulsemedia", s.OperationTag}, TotalBytes: s.TotalBytes, VerifiedBytes: s.TotalBytes, Complete: complete}
	if !complete {
		native.State = "downloading"
		native.VerifiedBytes = s.TotalBytes / 2
	}
	files := make([]ports.AutomaticTVRemoteFile, len(s.Files))
	for i, f := range s.Files {
		files[i] = ports.AutomaticTVRemoteFile{Index: i, Path: f.Path, Size: f.Size, CompletedBytes: f.Size, Wanted: i == 0}
	}
	return ports.AutomaticTVDownloadObservation{Complete: true, ClientID: s.ClientID, ClientStamp: s.ClientStamp, Before: []ports.AutomaticTVRemoteTask{native}, After: []ports.AutomaticTVRemoteTask{native}, Files: files}
}

// Keep90 proof fixtures independent of the next owner's91 publication.
func automaticTVObservationMigrations(t *testing.T) string {
	t.Helper()
	source := "../../../migrations"
	destination := filepath.Join(t.TempDir(), "migrations90")
	if e := os.Mkdir(destination, 0700); e != nil {
		t.Fatal(e)
	}
	entries, e := os.ReadDir(source)
	if e != nil {
		t.Fatal(e)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || len(name) < 5 || !strings.HasSuffix(name, ".sql") {
			continue
		}
		version, e := strconv.Atoi(name[:5])
		if e != nil || version > 90 {
			continue
		}
		raw, e := os.ReadFile(filepath.Join(source, name))
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(destination, name), raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
	return destination
}

func automaticTVObservationClock(t *testing.T, f *automaticTVObservationFixture, interval string) {
	t.Helper()
	execIntent(t, f.pool, `CREATE FUNCTION public.clock_timestamp() RETURNS timestamptz LANGUAGE SQL VOLATILE RETURN pg_catalog.clock_timestamp()+interval '`+interval+`'`)
	cfg := f.pool.Config()
	cfg.ConnConfig.RuntimeParams["search_path"] = "public,pg_catalog"
	pool, e := pgxpool.NewWithConfig(t.Context(), cfg)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(pool.Close)
	f.observer = postgres.NewAutomaticTVObservationStore(pool, idgen.New())
}
func automaticTVObservationCount(t *testing.T, f *automaticTVObservationFixture, table string) int {
	t.Helper()
	return intentCount(t, f.pool, "SELECT count(*) FROM "+table)
}
func TestAutomaticTVObservationFullInventoryPG(t *testing.T) {
	f := automaticTVSubmissionBase(t, false)
	encoded := func(s string) string { return strconv.Itoa(len(s)) + ":" + s }
	raw := "d4:infod5:filesl"
	for _, file := range []struct {
		name string
		size int
	}{{"Trusted.Native.Series.2024.S02E03.mkv", 1200}, {"release.nfo", 34}, {"empty.txt", 0}} {
		raw += "d6:lengthi" + strconv.Itoa(file.size) + "e4:pathl" + encoded(file.name) + "ee"
	}
	raw += "e4:name" + encoded("Trusted.Native.Series.2024") + "12:piece lengthi16384e6:pieces20:" + strings.Repeat("0", 20) + "ee"
	proof, e := ports.InspectAutomaticTVMetainfo(torrent.Inspector{}, []byte(raw))
	if e != nil {
		t.Fatal("actual multi-file metainfo", e)
	}
	round := f.start(t)
	candidate := tvExecutionCandidate(t, f, "observation-sidecars", "1080p", 5)
	hash := proof.InfoHash()
	candidate.InfoHash = &hash
	tvExecutionPage(t, f, round, candidate)
	lease := tvExecutionLease(t, f, round)
	decision, e := tvExecutionStore(f, acquisition.SelectAutomaticTVEpisode).FinalizeAutomaticTVRound(t.Context(), round.ID, lease)
	if e != nil || decision.State != "reserved" {
		t.Fatal("real authority", decision, e)
	}
	job, e := db.New(f.pool).ClaimJob(t.Context(), db.ClaimJobParams{Column1: []string{"automatic_tv_download_submission"}, Column2: "original-sender", Column3: 60000})
	if e != nil {
		t.Fatal(e)
	}
	req := ports.AutomaticTVSendRequest{OperationID: decision.OperationID, Lease: ports.JobLease{ID: job.ID, Owner: "original-sender", Token: job.LeaseToken}}
	original := automaticTVSubmissionFixture{automaticTVFixture: f, store: postgres.NewAutomaticTVSubmissionStore(f.pool, idgen.New(), crypto.NewSecretProtector(bytes.Repeat([]byte{27}, 32))), request: req, command: ports.PrepareAutomaticTVSubmissionCommand{Request: req, TaskID: uuid.New(), Metainfo: proof}}
	observer := automaticTVObservationFromSubmission(t, original, true)
	observer.claim(t)
	snapshot := observer.load(t)
	remote := automaticTVObservationRemote(snapshot, true)
	if len(remote.Files) != 3 || remote.Files[2].Size != 0 || remote.Files[2].Wanted {
		t.Fatal("sidecar fixture")
	}
	out, e := observer.observer.CommitAutomaticTVObservation(t.Context(), *snapshot, remote)
	if e != nil || !out.Complete || out.ReviewRequired {
		t.Fatal("full sidecars/unwanted/zero", out, e)
	}
}
func TestAutomaticTVObservationOriginalSendCrashPG(t *testing.T) {
	f := newAutomaticTVSubmissionFixture(t, false)
	f.prepare(t)
	if _, e := postgres.Migrate(t.Context(), f.pool.Config().ConnString(), automaticTVObservationMigrations(t)); e != nil {
		t.Fatal(e)
	}
	if _, e := f.pool.Exec(t.Context(), `SELECT pg_terminate_backend($1)`, int32(f.physical.Conn().PgConn().PID())); e != nil {
		t.Fatal(e)
	}
	_ = f.physical.Conn().Close(context.Background())
	f.physical.Release()
	f.physical = nil
	observer := postgres.NewAutomaticTVObservationStore(f.pool, idgen.New())
	execIntent(t, f.pool, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, f.request.Lease.ID)
	if n, e := observer.ScheduleAutomaticTVObservations(t.Context(), 100); e != nil || n != 0 {
		t.Fatal("dead sender PID is not physical completion", n, e)
	}
	if n := intentCount(t, f.pool, `SELECT count(*) FROM automatic_tv_observation_permit`); n != 0 {
		t.Fatal("sender crash created observer nonce", n)
	}
}
func TestAutomaticTVObservationFirstSendExpiredPG(t *testing.T) {
	f := automaticTVObservationFromSubmission(t, newAutomaticTVSubmissionFixture(t, true), true)
	f.claim(t)
	execIntent(t, f.pool, `UPDATE job SET lease_until=clock_timestamp()+interval '1 day' WHERE id=$1`, f.req.Lease.ID)
	automaticTVObservationClock(t, f, "6 minutes")
	snapshot := f.load(t)
	out, e := f.observer.CommitAutomaticTVObservation(t.Context(), *snapshot, automaticTVObservationRemote(snapshot, true))
	if e != nil || !out.Complete || out.ReviewRequired {
		t.Fatal("observation borrowed first-send deadline", out, e)
	}
}
func TestAutomaticTVObservationSchemaHistoryPG(t *testing.T) {
	t.Run("empty_up_down_up", func(t *testing.T) {
		dsn := pgtest.GetCluster(t).NewTestDB(t)
		if v, e := postgres.Migrate(t.Context(), dsn, automaticTVObservationMigrations(t)); e != nil || v != 90 {
			t.Fatal(v, e)
		}
		if v, e := postgres.Rollback(t.Context(), dsn, automaticTVObservationMigrations(t)); e != nil || v != 89 {
			t.Fatal(v, e)
		}
		if v, e := postgres.Migrate(t.Context(), dsn, automaticTVObservationMigrations(t)); e != nil || v != 90 {
			t.Fatal(v, e)
		}
	})
	t.Run("history_refuses", func(t *testing.T) {
		f := newAutomaticTVObservationFixture(t, true)
		f.claim(t)
		if _, e := postgres.Rollback(t.Context(), f.pool.Config().ConnString(), automaticTVObservationMigrations(t)); e == nil {
			t.Fatal("history downgraded")
		}
		for _, sql := range []string{`UPDATE automatic_tv_observation_permit SET backend_pid=backend_pid+1`, `DELETE FROM automatic_tv_observation_permit`, `TRUNCATE automatic_tv_observation_permit`, `UPDATE automatic_tv_observation_job SET task_id=task_id`, `DELETE FROM automatic_tv_observation_job`, `TRUNCATE automatic_tv_observation_job`} {
			if _, e := f.pool.Exec(t.Context(), sql); e == nil {
				t.Fatal("history mutable", sql)
			}
		}
		snapshot := f.load(t)
		if _, e := f.observer.CommitAutomaticTVObservation(t.Context(), *snapshot, automaticTVObservationRemote(snapshot, true)); e != nil {
			t.Fatal(e)
		}
		for _, sql := range []string{`UPDATE automatic_tv_observation_result SET state=state`, `DELETE FROM automatic_tv_observation_result`, `TRUNCATE automatic_tv_observation_result`} {
			if _, e := f.pool.Exec(t.Context(), sql); e == nil {
				t.Fatal("result mutable", sql)
			}
		}
	})
}

func TestAutomaticTVObservationCompletePG(t *testing.T) {
	for _, record := range []bool{true, false} {
		t.Run(fmt.Sprint("sender_result_", record), func(t *testing.T) {
			f := newAutomaticTVObservationFixture(t, record)
			f.claim(t)
			s := f.load(t)
			if raw, _ := json.Marshal(s); string(raw) != "{}" {
				t.Fatal("snapshot exposed transient authority")
			}
			out, e := f.observer.CommitAutomaticTVObservation(t.Context(), *s, automaticTVObservationRemote(s, true))
			if e != nil || !out.Complete || out.ReviewRequired || out.State != "completed" {
				t.Fatalf("complete %+v %v", out, e)
			}
			if n := automaticTVObservationCount(t, f, "automatic_tv_observation_result"); n != 1 {
				t.Fatal(n)
			}
			if n := intentCount(t, f.pool, `SELECT count(*) FROM job WHERE kind='download_organization' AND state='queued' AND payload-ARRAY['operation_id','task_id','automatic_tv_authority','observation_job_id','inventory_stamp']='{}'::jsonb AND payload->>'inventory_stamp'=$1`, out.EvidenceStamp); n != 1 {
				t.Fatal("organization binding", n)
			}
			if _, e = f.observer.CommitAutomaticTVObservation(t.Context(), *s, automaticTVObservationRemote(s, true)); !errors.Is(e, ports.ErrAutomaticTVObservationStale) {
				t.Fatal("duplicate callback", e)
			}
			if n := automaticTVObservationCount(t, f, "unit_fact"); n != 0 {
				t.Fatal("observation invented imported facts", n)
			}
		})
	}
}
func TestAutomaticTVObservationTrackingFreshJobPG(t *testing.T) {
	f := newAutomaticTVObservationFixture(t, true)
	f.claim(t)
	s := f.load(t)
	original := f.req.Lease.ID
	out, e := f.observer.CommitAutomaticTVObservation(t.Context(), *s, automaticTVObservationRemote(s, false))
	if e != nil || out.Complete || out.ReviewRequired || out.State != "tracking" {
		t.Fatalf("tracking %+v %v", out, e)
	}
	if n, e := f.observer.ScheduleAutomaticTVObservations(t.Context(), 100); e != nil || n != 0 {
		t.Fatal("poll too early or physical return outstanding", n, e)
	}
	f.finishPermit(t)
	if n, e := f.observer.ScheduleAutomaticTVObservations(t.Context(), 100); e != nil || n != 0 {
		t.Fatal("two second floor", n, e)
	}
	var seconds float64
	if e = f.pool.QueryRow(t.Context(), `SELECT EXTRACT(EPOCH FROM next_poll_at-created_at) FROM automatic_tv_observation_result WHERE job_id=$1`, original).Scan(&seconds); e != nil || seconds < 2 {
		t.Fatal("poll interval", seconds, e)
	}
	// The immutable result is not rewritten to accelerate time. Force only the
	// database's clock function in this isolated test database.
	automaticTVObservationClock(t, f, "3 seconds")
	if n, e := f.observer.ScheduleAutomaticTVObservations(t.Context(), 100); e != nil || n != 1 {
		t.Fatal("fresh poll", n, e)
	}
	if n := intentCount(t, f.pool, `SELECT count(*) FROM automatic_tv_observation_job WHERE job_id<>$1`, original); n != 1 {
		t.Fatal("resurrected original", n)
	}
}
func TestAutomaticTVObservationUnknownKeepsProtectionPG(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ports.AutomaticTVDownloadObservation)
	}{
		{"nil_before", func(o *ports.AutomaticTVDownloadObservation) { o.Before = nil }},
		{"absent", func(o *ports.AutomaticTVDownloadObservation) {
			o.Before = []ports.AutomaticTVRemoteTask{}
			o.After = []ports.AutomaticTVRemoteTask{}
		}},
		{"ambiguous", func(o *ports.AutomaticTVDownloadObservation) { o.After = append(o.After, o.After[0]) }},
		{"wrong_hash", func(o *ports.AutomaticTVDownloadObservation) { o.Before[0].InfoHash = strings.Repeat("f", 40) }},
		{"wrong_label", func(o *ports.AutomaticTVDownloadObservation) { o.After[0].Tags = []string{"pulsemedia"} }},
		{"wrong_path", func(o *ports.AutomaticTVDownloadObservation) { o.After[0].SavePath += "/other" }},
		{"nil_files", func(o *ports.AutomaticTVDownloadObservation) { o.Files = nil }},
		{"empty_files", func(o *ports.AutomaticTVDownloadObservation) { o.Files = []ports.AutomaticTVRemoteFile{} }},
		{"extra_file", func(o *ports.AutomaticTVDownloadObservation) {
			o.Files = append(o.Files, ports.AutomaticTVRemoteFile{Index: 1, Path: "note.nfo"})
		}},
		{"duplicate_index", func(o *ports.AutomaticTVDownloadObservation) { o.Files[0].Index = 1 }},
		{"negative_index", func(o *ports.AutomaticTVDownloadObservation) { o.Files[0].Index = -1 }},
		{"wrong_size", func(o *ports.AutomaticTVDownloadObservation) { o.Files[0].Size++ }},
		{"partial_file", func(o *ports.AutomaticTVDownloadObservation) { o.Files[0].CompletedBytes-- }},
		{"wrong_episode", func(o *ports.AutomaticTVDownloadObservation) {
			o.Files[0].Path = "Trusted.Native.Series.2024.S02E04.mkv"
		}},
		{"traversal", func(o *ports.AutomaticTVDownloadObservation) { o.Files[0].Path = "../" + o.Files[0].Path }},
		{"false_verified", func(o *ports.AutomaticTVDownloadObservation) { o.After[0].VerifiedBytes-- }},
		{"unstable_complete", func(o *ports.AutomaticTVDownloadObservation) { o.Before[0].Complete = false }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAutomaticTVObservationFixture(t, true)
			f.claim(t)
			s := f.load(t)
			remote := automaticTVObservationRemote(s, true)
			tc.mutate(&remote)
			out, e := f.observer.CommitAutomaticTVObservation(t.Context(), *s, remote)
			if e != nil || !out.ReviewRequired || out.Complete || out.State != "unknown" {
				t.Fatalf("unknown %+v %v", out, e)
			}
			f.finishPermit(t)
			if n, e := f.observer.ScheduleAutomaticTVObservations(t.Context(), 100); e != nil || n != 0 {
				t.Fatal("review auto retried", n, e)
			}
			if n := intentCount(t, f.pool, `SELECT count(*) FROM acquisition_reservation WHERE released_at IS NULL`); n != 1 {
				t.Fatal("unknown released protection", n)
			}
			if n := intentCount(t, f.pool, `SELECT count(*) FROM job WHERE kind='download_organization'`); n != 0 {
				t.Fatal("unknown organized", n)
			}
		})
	}
}
func TestAutomaticTVObservationSealAndLeasePG(t *testing.T) {
	f := newAutomaticTVObservationFixture(t, true)
	f.claim(t)
	s := f.load(t)
	mutations := []func(*ports.AutomaticTVObservationSnapshot){
		func(s *ports.AutomaticTVObservationSnapshot) { s.InfoHash = strings.Repeat("f", 40) }, func(s *ports.AutomaticTVObservationSnapshot) { s.Request.TaskID = uuid.New() }, func(s *ports.AutomaticTVObservationSnapshot) { s.Request.OperationID = uuid.New() }, func(s *ports.AutomaticTVObservationSnapshot) { s.Request.PhysicalPermitID = uuid.New() }, func(s *ports.AutomaticTVObservationSnapshot) { s.Request.Lease.Token++ }, func(s *ports.AutomaticTVObservationSnapshot) { s.Authority.DecisionStamp = strings.Repeat("0", 64) }, func(s *ports.AutomaticTVObservationSnapshot) { s.Authority.Target.Title = "tampered" }, func(s *ports.AutomaticTVObservationSnapshot) { s.SavePath += "/wrong" }, func(s *ports.AutomaticTVObservationSnapshot) { s.TotalBytes++ }, func(s *ports.AutomaticTVObservationSnapshot) { s.MetainfoSHA256[0] ^= 1 }, func(s *ports.AutomaticTVObservationSnapshot) { s.Files = slices.Clone(s.Files); s.Files[0].Size++ }, func(s *ports.AutomaticTVObservationSnapshot) { s.Video.Size++ }, func(s *ports.AutomaticTVObservationSnapshot) { s.SendPhysicalAttemptID = uuid.New() }, func(s *ports.AutomaticTVObservationSnapshot) { s.OperationTag = "pulsemedia" },
	}
	for i, mutate := range mutations {
		altered := *s
		mutate(&altered)
		if _, e := f.observer.CommitAutomaticTVObservation(t.Context(), altered, automaticTVObservationRemote(s, true)); !errors.Is(e, ports.ErrAutomaticTVObservationStale) {
			t.Fatal("tamper", i, e)
		}
	}
	if n := automaticTVObservationCount(t, f, "automatic_tv_observation_result"); n != 0 {
		t.Fatal("stale wrote facts", n)
	}
	s.Files[0].Size++
	s.Authority.Target.Rules[0] = 'x'
	for k := range s.Authority.Target.IndexerVersions {
		s.Authority.Target.IndexerVersions[k] = 999
	}
	fresh := f.load(t)
	if fresh.Files[0].Size != f.command.Metainfo.TotalBytes() {
		t.Fatal("load aliased files")
	}
	execIntent(t, f.pool, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, f.req.Lease.ID)
	if _, e := f.observer.CommitAutomaticTVObservation(t.Context(), *fresh, automaticTVObservationRemote(fresh, true)); !errors.Is(e, ports.ErrAutomaticTVObservationStale) {
		t.Fatal("old lease", e)
	}
}
func TestAutomaticTVObservationCurrentEligibilityPG(t *testing.T) {
	cases := []struct{ name, sql string }{
		{"disabled_owner", `UPDATE app_user SET is_active=false`}, {"paused", `UPDATE subscription SET state='paused'`}, {"intent", `UPDATE subscription SET intent_version=intent_version+1`}, {"root_path", `UPDATE library_root SET path=path||'/replacement'`}, {"root_generation", `UPDATE library_root SET generation=generation+1`}, {"offline_root", `UPDATE library_root SET health='offline'`}, {"source_disabled", `UPDATE indexer_lifecycle SET enabled=false`}, {"source_version", `UPDATE indexer SET config_version=config_version+1`}, {"client_changed", `UPDATE download_client SET username='changed'`}, {"unit_changed", `UPDATE media_unit SET metadata_version=metadata_version+1`}, {"wanted_skipped", `UPDATE wanted_item SET is_skipped=true,user_skipped=true`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAutomaticTVObservationFixture(t, true)
			f.claim(t)
			s := f.load(t)
			execIntent(t, f.pool, tc.sql)
			if tc.name != "client_changed" {
				reloaded := f.load(t)
				if reloaded.Seal != s.Seal {
					t.Fatal("business change rewrote original physical proof")
				}
			}
			out, e := f.observer.CommitAutomaticTVObservation(t.Context(), *s, automaticTVObservationRemote(s, true))
			if e != nil || !out.Complete || !out.ReviewRequired || out.State != "completed" {
				t.Fatalf("physical/business split %+v %v", out, e)
			}
			if n := intentCount(t, f.pool, `SELECT count(*) FROM job WHERE kind='download_organization'`); n != 0 {
				t.Fatal("ineligible organized", n)
			}
		})
	}
}
func TestAutomaticTVObservationChangedClientCannotLoadPG(t *testing.T) {
	f := newAutomaticTVObservationFixture(t, true)
	f.claim(t)
	execIntent(t, f.pool, `UPDATE download_client SET username='replacement-instance'`)
	if _, e := f.observer.LoadAutomaticTVObservation(t.Context(), f.req); !errors.Is(e, ports.ErrAutomaticTVNeedsObservation) {
		t.Fatal("queried replacement client", e)
	}
}
func TestAutomaticTVObservationSchedulerRacePG(t *testing.T) {
	f := newAutomaticTVObservationFixture(t, true)
	for _, limit := range []int32{-1, 0, 101} {
		if n, e := f.observer.ScheduleAutomaticTVObservations(t.Context(), limit); e == nil || n != 0 {
			t.Fatal("limit", limit, n, e)
		}
	}
	var wg sync.WaitGroup
	counts := make(chan int, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			n, e := postgres.NewAutomaticTVObservationStore(f.pool, idgen.New()).ScheduleAutomaticTVObservations(t.Context(), 1)
			counts <- n
			errs <- e
		})
	}
	wg.Wait()
	close(counts)
	close(errs)
	total := 0
	for n := range counts {
		total += n
	}
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if total != 1 {
		t.Fatal("dedupe", total)
	}
	if n := automaticTVObservationCount(t, f, "automatic_tv_observation_job"); n != 1 {
		t.Fatal(n)
	}
}
func TestAutomaticTVObservationPhysicalCrashHoldPG(t *testing.T) {
	f := newAutomaticTVObservationFixture(t, true)
	f.claim(t)
	s := f.load(t)
	if _, e := f.pool.Exec(t.Context(), `SELECT pg_terminate_backend($1)`, int32(f.conn.Conn().PgConn().PID())); e != nil {
		t.Fatal(e)
	}
	_ = f.conn.Conn().Close(context.Background())
	f.conn.Release()
	f.conn = nil
	if _, e := f.observer.CommitAutomaticTVObservation(t.Context(), *s, automaticTVObservationRemote(s, true)); !errors.Is(e, ports.ErrAutomaticTVObservationStale) {
		t.Fatal("lost physical lock", e)
	}
	execIntent(t, f.pool, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, f.req.Lease.ID)
	if n, e := f.observer.RecoverAutomaticTVObservations(t.Context(), 100); e != nil || n != 0 {
		t.Fatal("crash nonce auto finished", n, e)
	}
	if n, e := f.observer.ScheduleAutomaticTVObservations(t.Context(), 100); e != nil || n != 0 {
		t.Fatal("crash nonce rescheduled", n, e)
	}
}
func TestAutomaticTVObservationRecoverySameJobPG(t *testing.T) {
	for _, exhausted := range []bool{false, true} {
		t.Run(fmt.Sprint(exhausted), func(t *testing.T) {
			f := newAutomaticTVObservationFixture(t, true)
			f.claim(t)
			id, token := f.req.Lease.ID, f.req.Lease.Token
			oldSnapshot := f.load(t)
			f.finishPermit(t)
			execIntent(t, f.pool, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second',max_attempts=CASE WHEN $2 THEN attempts ELSE max_attempts END WHERE id=$1`, id, exhausted)
			if n, e := f.observer.RecoverAutomaticTVObservations(t.Context(), 100); e != nil || n != 1 {
				t.Fatal(n, e)
			}
			j, e := db.New(f.pool).GetJobByID(t.Context(), id)
			if e != nil || j.LeaseToken != token+1 || j.Attempts != 1 {
				t.Fatal("original job/token/attempts", j, e)
			}
			expected := "queued"
			if exhausted {
				expected = "dead"
			}
			if j.State != expected {
				t.Fatal(j.State)
			}
			if n, e := f.observer.ScheduleAutomaticTVObservations(t.Context(), 100); e != nil || n != 0 {
				t.Fatal("exhaustion resurrected", n, e)
			}
			if !exhausted {
				newJob, e := db.New(f.pool).ClaimJob(t.Context(), db.ClaimJobParams{Column1: []string{ports.AutomaticTVObservationJobKind}, Column2: "recovered-observer", Column3: 60000})
				if e != nil || newJob.ID != id || newJob.Attempts != 2 || newJob.LeaseToken <= token {
					t.Fatal("recovered original job", newJob, e)
				}
				f.req.Lease = ports.JobLease{ID: id, Owner: "recovered-observer", Token: newJob.LeaseToken}
				f.permit(t)
				if _, e = f.observer.CommitAutomaticTVObservation(t.Context(), *oldSnapshot, automaticTVObservationRemote(oldSnapshot, true)); !errors.Is(e, ports.ErrAutomaticTVObservationStale) {
					t.Fatal("recovered job accepted old token", e)
				}
				snapshot := f.load(t)
				if out, e := f.observer.CommitAutomaticTVObservation(t.Context(), *snapshot, automaticTVObservationRemote(snapshot, true)); e != nil || !out.Complete {
					t.Fatal("original R0 recovery result", out, e)
				}
			}
		})
	}
}
func TestAutomaticTVObservationTailClockRollbackPG(t *testing.T) {
	for _, label := range []string{"audit_log", "outbox_event", "job", "deferred_job"} {
		t.Run(label, func(t *testing.T) {
			table := label
			if label == "deferred_job" {
				table = "job"
			}
			f := newAutomaticTVObservationFixture(t, false)
			f.claim(t)
			execIntent(t, f.pool, `CREATE TABLE observation_test_clock(seconds int NOT NULL); INSERT INTO observation_test_clock VALUES(0)`)
			execIntent(t, f.pool, `CREATE FUNCTION public.clock_timestamp() RETURNS timestamptz LANGUAGE SQL VOLATILE RETURN pg_catalog.clock_timestamp()+(SELECT seconds FROM observation_test_clock)*interval '1 second'`)
			cfg := f.pool.Config()
			cfg.ConnConfig.RuntimeParams["search_path"] = "public,pg_catalog"
			pool, e := pgxpool.NewWithConfig(t.Context(), cfg)
			if e != nil {
				t.Fatal(e)
			}
			t.Cleanup(pool.Close)
			f.observer = postgres.NewAutomaticTVObservationStore(pool, idgen.New())
			snapshot := f.load(t)
			condition := "TRUE"
			event := "INSERT"
			if table == "job" {
				condition = "NEW.kind='automatic_tv_observation' AND NEW.state='succeeded'"
				event = "UPDATE"
			}
			execIntent(t, f.pool, `CREATE FUNCTION observation_advance_clock() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF `+condition+` THEN UPDATE observation_test_clock SET seconds=600; END IF; RETURN NEW; END $$`)
			triggerKind := "TRIGGER"
			deferred := ""
			if label == "deferred_job" {
				triggerKind = "CONSTRAINT TRIGGER"
				deferred = " DEFERRABLE INITIALLY DEFERRED"
			}
			execIntent(t, f.pool, `CREATE `+triggerKind+` observation_advance_clock AFTER `+event+` ON `+table+deferred+` FOR EACH ROW EXECUTE FUNCTION observation_advance_clock()`)
			if _, e = f.observer.CommitAutomaticTVObservation(t.Context(), *snapshot, automaticTVObservationRemote(snapshot, true)); !errors.Is(e, ports.ErrAutomaticTVObservationStale) {
				t.Fatal("tail DB clock expiry", e)
			}
			if n := automaticTVObservationCount(t, f, "automatic_tv_observation_result"); n != 0 {
				t.Fatal("expired result persisted", n)
			}
			if n := intentCount(t, f.pool, `SELECT count(*) FROM download_task WHERE state='submitting'`); n != 1 {
				t.Fatal("expired task changed", n)
			}
			if n := intentCount(t, f.pool, `SELECT count(*) FROM job WHERE kind='download_organization'`); n != 0 {
				t.Fatal("expired organization persisted", n)
			}
			if n := intentCount(t, f.pool, `SELECT seconds FROM observation_test_clock`); n != 0 {
				t.Fatal("clock not rolled back", n)
			}
			execIntent(t, f.pool, `DROP TRIGGER observation_advance_clock ON `+table)
			if out, e := f.observer.CommitAutomaticTVObservation(t.Context(), *snapshot, automaticTVObservationRemote(snapshot, true)); e != nil || !out.Complete {
				t.Fatal("original callback after rollback", out, e)
			}
		})
	}
}
func TestAutomaticTVObservationGenericCompletionGuardPG(t *testing.T) {
	f := newAutomaticTVObservationFixture(t, true)
	for _, sql := range []string{
		`UPDATE download_task SET state='completed',progress=1,total_bytes=1234,downloaded_bytes=1234`,
		`UPDATE download_task SET info_hash=repeat('f',40)`,
		`UPDATE download_task SET save_path=save_path||'/other'`,
	} {
		if _, e := f.pool.Exec(t.Context(), sql); e == nil {
			t.Fatal("generic task bypass", sql)
		}
	}
	if _, e := db.New(f.pool).EnqueueDownloadOrganization(t.Context(), db.EnqueueDownloadOrganizationParams{ID: uuid.New(), TaskID: f.command.TaskID}); e == nil {
		t.Fatal("generic organization bypass")
	}
	f.claim(t)
	snapshot := f.load(t)
	if out, e := f.observer.CommitAutomaticTVObservation(t.Context(), *snapshot, automaticTVObservationRemote(snapshot, true)); e != nil || !out.Complete {
		t.Fatal("guard blocked legitimate observer", out, e)
	}
	if _, e := f.pool.Exec(t.Context(), `UPDATE download_task SET content_path=content_path||'/replacement'`); e == nil {
		t.Fatal("completed content path mutated without evidence")
	}
}

func TestAutomaticTVObservationRecoveryPhysicalSessionPG(t *testing.T) {
	f := newAutomaticTVObservationFixture(t, true)
	f.claim(t)
	f.finishPermit(t)
	conn, e := f.pool.Acquire(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	digest := sha256.Sum256([]byte("pulsemedia.automatic-tv.physical.v1:" + f.request.OperationID.String()))
	key := int64(binary.BigEndian.Uint64(digest[:8]))
	var held bool
	if e = conn.QueryRow(t.Context(), `SELECT pg_try_advisory_lock($1)`, key).Scan(&held); e != nil || !held {
		conn.Release()
		t.Fatal(held, e)
	}
	t.Cleanup(func() { _, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, key); conn.Release() })
	execIntent(t, f.pool, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, f.req.Lease.ID)
	if n, e := f.observer.RecoverAutomaticTVObservations(t.Context(), 100); e != nil || n != 0 {
		t.Fatal("recovery raced physical permit creation", n, e)
	}
	if _, e = conn.Exec(t.Context(), `SELECT pg_advisory_unlock($1)`, key); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	counts := make(chan int, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			n, e := postgres.NewAutomaticTVObservationStore(f.pool, idgen.New()).RecoverAutomaticTVObservations(t.Context(), 100)
			counts <- n
			errs <- e
		})
	}
	wg.Wait()
	close(counts)
	close(errs)
	total := 0
	for n := range counts {
		total += n
	}
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if total != 1 {
		t.Fatal("recovery not unique", total)
	}
	for _, limit := range []int32{-1, 0, 101} {
		if n, e := f.observer.RecoverAutomaticTVObservations(t.Context(), limit); e == nil || n != 0 {
			t.Fatal("invalid recovery limit", limit, n, e)
		}
	}
}
func TestAutomaticTVObservationRestoreIsolationPG(t *testing.T) {
	f := newAutomaticTVObservationFixture(t, true)
	f.claim(t)
	snapshot := f.load(t)
	if e := postgres.ActivateRestoreIsolation(t.Context(), f.pool, uuid.New()); e != nil {
		t.Fatal(e)
	}
	if _, e := f.observer.LoadAutomaticTVObservation(t.Context(), f.req); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal("isolated load", e)
	}
	if _, e := f.observer.CommitAutomaticTVObservation(t.Context(), *snapshot, automaticTVObservationRemote(snapshot, true)); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal("isolated commit", e)
	}
	if n, e := f.observer.ScheduleAutomaticTVObservations(t.Context(), 100); !errors.Is(e, ports.ErrUnavailable) || n != 0 {
		t.Fatal("isolated schedule", n, e)
	}
	if n, e := f.observer.RecoverAutomaticTVObservations(t.Context(), 100); !errors.Is(e, ports.ErrUnavailable) || n != 0 {
		t.Fatal("isolated recover", n, e)
	}
	if n := automaticTVObservationCount(t, f, "automatic_tv_observation_result"); n != 0 {
		t.Fatal("isolated publication", n)
	}
}

func TestAutomaticTVObservationTailRollbackPG(t *testing.T) {
	for _, table := range []string{"audit_log", "outbox_event", "job"} {
		t.Run(table, func(t *testing.T) {
			f := newAutomaticTVObservationFixture(t, false)
			f.claim(t)
			s := f.load(t)
			condition := "TRUE"
			event := "INSERT"
			if table == "job" {
				condition = "NEW.kind='automatic_tv_observation' AND NEW.state='succeeded'"
				event = "UPDATE"
			}
			execIntent(t, f.pool, `CREATE FUNCTION observation_tail_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF `+condition+` THEN RAISE EXCEPTION 'owned observation fault'; END IF; RETURN NEW; END $$`)
			execIntent(t, f.pool, `CREATE TRIGGER observation_tail_fault BEFORE `+event+` ON `+table+` FOR EACH ROW EXECUTE FUNCTION observation_tail_fault()`)
			if _, e := f.observer.CommitAutomaticTVObservation(t.Context(), *s, automaticTVObservationRemote(s, true)); e == nil {
				t.Fatal("tail accepted")
			}
			if n := automaticTVObservationCount(t, f, "automatic_tv_observation_result"); n != 0 {
				t.Fatal("partial result", n)
			}
			if n := intentCount(t, f.pool, `SELECT count(*) FROM download_task WHERE state='submitting'`); n != 1 {
				t.Fatal("partial task", n)
			}
			if n := intentCount(t, f.pool, `SELECT count(*) FROM job WHERE kind='download_organization'`); n != 0 {
				t.Fatal("partial organization", n)
			}
			execIntent(t, f.pool, `DROP TRIGGER observation_tail_fault ON `+table)
			out, e := f.observer.CommitAutomaticTVObservation(t.Context(), *s, automaticTVObservationRemote(s, true))
			if e != nil || !out.Complete {
				t.Fatal("original result recovery", out, e)
			}
		})
	}
}
