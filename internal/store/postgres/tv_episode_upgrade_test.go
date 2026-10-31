package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/google/uuid"
)

type tvUpgradeTestNative struct {
	proof   *ports.AutomaticTVMetainfoProof
	calls   atomic.Int32
	started chan struct{}
	unblock chan struct{}
	before  func()
	mu      sync.Mutex
	added   ports.AddTorrentParam
}

func (n *tvUpgradeTestNative) FetchTVEpisodeUpgrade(context.Context, string, []byte, []byte) (*ports.AutomaticTVMetainfoProof, error) {
	return n.proof, nil
}
func (n *tvUpgradeTestNative) OpenTVEpisodeUpgradeClient(context.Context, ports.TVEpisodeUpgradeNativeConfig) (ports.TVEpisodeUpgradeClient, func(), error) {
	return n, func() {}, nil
}
func (n *tvUpgradeTestNative) AddTorrent(ctx context.Context, p ports.AddTorrentParam) error {
	n.calls.Add(1)
	n.mu.Lock()
	n.added = p
	n.mu.Unlock()
	if n.started != nil {
		close(n.started)
	}
	if n.unblock != nil {
		<-n.unblock
	}
	if n.before != nil {
		n.before()
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if e := os.MkdirAll(p.SavePath, 0755); e != nil {
		return e
	}
	return os.WriteFile(filepath.Join(p.SavePath, n.proof.Files()[0].Path), bytes.Repeat([]byte("n"), 1234), 0600)
}
func (n *tvUpgradeTestNative) ListTorrents(context.Context, string) ([]*ports.TorrentInfo, error) {
	n.mu.Lock()
	p := n.added
	n.mu.Unlock()
	return []*ports.TorrentInfo{{InfoHash: p.InfoHash, SavePath: p.SavePath, TotalBytes: 1234, DownloadedBytes: 1234, Progress: 1, State: "seeding", Tags: p.Tags}}, nil
}
func (n *tvUpgradeTestNative) GetTorrentFiles(context.Context, string) ([]ports.TorrentFile, error) {
	return []ports.TorrentFile{{Index: 0, Name: n.proof.Files()[0].Path, SizeBytes: 1234, Progress: 1}}, nil
}

type tvUpgradeTestPublisher struct{}

func (tvUpgradeTestPublisher) PublishImported(ctx context.Context, q *db.Queries, id string, payload any) (*db.OutboxEvent, error) {
	raw, e := json.Marshal(payload)
	if e != nil {
		return nil, e
	}
	row, e := q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: uuid.New(), AggregateType: "media_unit", AggregateID: id, EventType: "unit.imported", Payload: raw, Seq: 1})
	return &row, e
}

type tvUpgradeFixture struct {
	automaticTVFixture
	repo     *postgres.TVEpisodeUpgradeStore
	service  *acquisition.TVEpisodeUpgradeService
	native   *tvUpgradeTestNative
	request  ports.TVEpisodeUpgradePreviewRequest
	rootPath string
}

func newTVUpgradeFixture(t *testing.T) *tvUpgradeFixture {
	t.Helper()
	b := newAutomaticTVFixture(t)
	if _, e := b.pool.Exec(t.Context(), "SELECT 1 FROM tv_episode_upgrade_authority LIMIT 1"); e != nil {
		t.Fatal("migration137 required", e)
	}
	execIntent(t, b.pool, "UPDATE app_user SET role='admin' WHERE id=$1", b.actor.ID)
	b.actor.Role = ports.RoleAdmin
	rootPath := filepath.Join(b.path, "tv")
	if e := os.MkdirAll(rootPath, 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(rootPath, ".pulsemedia_root"), []byte("owned MP22 root"), 0600); e != nil {
		t.Fatal(e)
	}
	old := "Trusted.Native.Series.2024.S02E03.1080p.WEB-DL.H264.mkv"
	if e := os.WriteFile(filepath.Join(rootPath, old), bytes.Repeat([]byte("o"), 100), 0600); e != nil {
		t.Fatal(e)
	}
	stat, e := os.Stat(filepath.Join(rootPath, old))
	if e != nil {
		t.Fatal(e)
	}
	st := stat.Sys().(*syscall.Stat_t)
	rootStat, e := os.Stat(rootPath)
	if e != nil {
		t.Fatal(e)
	}
	rst := rootStat.Sys().(*syscall.Stat_t)
	scan := uuid.New()
	execIntent(t, b.pool, `UPDATE library_root SET marker_filename='.pulsemedia_root' WHERE id=$1`, b.tv)
	execIntent(t, b.pool, `INSERT INTO scan_run(id,root_id,scan_token,generation,start_mutation_seq,state,finished_at) SELECT $1,id,scan_token,generation,mutation_seq,'succeeded',clock_timestamp() FROM library_root WHERE id=$2`, scan, b.tv)
	execIntent(t, b.pool, `INSERT INTO scan_finalization(scan_run_id,root_id,expected_path,expected_media_type,expected_marker_filename,expected_health_revision,root_device,root_inode,exhausted,completed_at) SELECT $1,id,path,media_type,marker_filename,health_revision,$3,$4,true,clock_timestamp() FROM library_root WHERE id=$2`, scan, b.tv, int64(rst.Dev), int64(rst.Ino))
	rules, _ := json.Marshal(quality.PolicyRules{CutoffScore: 1000, ResolutionOrder: map[quality.Resolution]int{quality.Res1080p: 300, quality.Res2160p: 400}, SourceOrder: map[quality.Source]int{quality.SourceWEBDL: 40}})
	execIntent(t, b.pool, "INSERT INTO policy(id,name) VALUES('mp22','MP22')")
	execIntent(t, b.pool, "INSERT INTO policy_revision(id,policy_id,revision,rules) VALUES($1,'mp22',1,$2)", uuid.New(), rules)
	execIntent(t, b.pool, "UPDATE subscription SET quality_policy_id='mp22',quality_policy_revision=1,upgrade_mode='stop_at_cutoff' WHERE id=$1", b.sub)
	execIntent(t, b.pool, "UPDATE wanted_item SET intent_version=(SELECT intent_version FROM subscription WHERE id=$1) WHERE subscription_id=$1", b.sub)
	mapping, _ := json.Marshal(map[string]string{rootPath: rootPath})
	execIntent(t, b.pool, "UPDATE download_client SET remote_path_map=$2 WHERE id=$1", b.client, mapping)
	file, fact, candidate := uuid.New(), uuid.New(), uuid.New()
	execIntent(t, b.pool, `INSERT INTO library_file(id,root_id,rel_path,size_bytes,mtime_ns,file_fingerprint) VALUES($1,$2,$3,100,$4,$5)`, file, b.tv, old, stat.ModTime().UnixNano(), fmt.Sprintf("stat.v1:%d:%d:%d", st.Ino, stat.Size(), stat.ModTime().UnixNano()))
	execIntent(t, b.pool, "INSERT INTO library_file_unit(file_id,unit_id) VALUES($1,$2)", file, b.unit)
	profile, _ := json.Marshal(media.Recognize(old).Quality)
	execIntent(t, b.pool, "INSERT INTO unit_fact(id,unit_id,root_id,source_file_id,stage,quality_profile) VALUES($1,$2,$3,$4,'imported',$5)", fact, b.unit, b.tv, file, profile)
	name := "Trusted.Native.Series.2024.S02E03.2160p.WEB-DL.H264.mkv"
	profile, _ = json.Marshal(media.Recognize(name).Quality)
	q := db.New(b.pool)
	_, e = q.UpsertReleaseCandidate(t.Context(), db.UpsertReleaseCandidateParams{ID: candidate, IndexerID: b.indexer, Guid: "mp22-candidate", Title: name, LinkEncrypted: []byte("owned-ciphertext"), SizeBytes: 1234, QualityProfile: profile, SourceConfigVersion: 1})
	if e != nil {
		t.Fatal(e)
	}
	query := uuid.New()
	execIntent(t, b.pool, "INSERT INTO indexer_query(id,indexer_id,query_type,keyword,config_version,state,finished_at) VALUES($1,$2,'search','mp22',1,'succeeded',clock_timestamp())", query, b.indexer)
	execIntent(t, b.pool, "INSERT INTO indexer_query_candidate(query_id,candidate_id) VALUES($1,$2)", query, candidate)
	native := &tvUpgradeTestNative{proof: automaticTVSubmissionMetainfo(t, name)}
	fs := rootfs.NewFilesystem()
	repo := postgres.NewTVEpisodeUpgradeStore(b.pool, idgen.New(), crypto.NewSecretProtector(bytes.Repeat([]byte{27}, 32)), native, fs, fs, tvUpgradeTestPublisher{})
	return &tvUpgradeFixture{automaticTVFixture: b, repo: repo, service: acquisition.NewTVEpisodeUpgradeService(repo), native: native, request: ports.TVEpisodeUpgradePreviewRequest{SubscriptionID: b.sub, UnitID: b.unit, CurrentFileID: file, CandidateID: candidate}, rootPath: rootPath}
}
func (f *tvUpgradeFixture) command(t *testing.T, action string, op uuid.UUID, version, key string) *ports.TVEpisodeUpgradeResult {
	t.Helper()
	out, e := f.service.Command(t.Context(), f.actor, ports.TVEpisodeUpgradeCommand{TVEpisodeUpgradePreviewRequest: f.request, Action: action, OperationID: op, ExpectedVersion: version, Confirmed: true}, key)
	if e != nil {
		t.Fatalf("%s: %v", action, e)
	}
	if out == nil || out.ExecutionAllowed {
		t.Fatalf("invalid %s receipt: %+v", action, out)
	}
	return out
}
func (f *tvUpgradeFixture) reserved(t *testing.T) *ports.TVEpisodeUpgradeResult {
	t.Helper()
	p, e := f.service.Preview(t.Context(), f.actor, f.request)
	if e != nil || p == nil || !p.Improvement || p.Reason != "" {
		t.Fatalf("preview: %+v %v", p, e)
	}
	return f.command(t, "reserve", uuid.Nil, p.Version, "reserve")
}
func (f *tvUpgradeFixture) prepared(t *testing.T) *ports.TVEpisodeUpgradeResult {
	t.Helper()
	r := f.reserved(t)
	return f.command(t, "prepare", r.OperationID, r.Version, "prepare")
}
func TestTVEpisodeUpgradeCompletePG(t *testing.T) {
	f := newTVUpgradeFixture(t)
	r := f.prepared(t)
	send := f.command(t, "send", r.OperationID, r.Version, "send")
	if send.Status != "tracking" {
		t.Fatal(send)
	}
	// New repository instances recover the same explicit command without another Add.
	again := f.command(t, "send", r.OperationID, r.Version, "send")
	if again.Status != send.Status || f.native.calls.Load() != 1 {
		t.Fatal("send replay")
	}
	observed := f.command(t, "observe", r.OperationID, r.Version, "observe")
	if observed.Status != "organization_review_required" {
		t.Fatal(observed)
	}
	review := f.command(t, "organization-preview", r.OperationID, r.Version, "org-preview")
	confirm := f.command(t, "organization-confirm", r.OperationID, review.Version, "org-confirm")
	imported := f.command(t, "organize", r.OperationID, confirm.Version, "organize")
	if imported.Status != "imported_pending_retirement" || imported.NewFileID == uuid.Nil {
		t.Fatal(imported)
	}
	var old string
	if e := f.pool.QueryRow(t.Context(), "SELECT rel_path FROM library_file WHERE id=$1", f.request.CurrentFileID).Scan(&old); e != nil {
		t.Fatal(e)
	}
	if data, e := os.ReadFile(filepath.Join(f.rootPath, old)); e != nil || !bytes.Equal(data, bytes.Repeat([]byte("o"), 100)) {
		t.Fatal("old video changed before retirement", e)
	}
	retirement := f.command(t, "retirement-preview", r.OperationID, imported.Version, "retire-preview")
	approved := f.command(t, "retirement-confirm", r.OperationID, retirement.Version, "retire-confirm")
	retired := f.command(t, "retire", r.OperationID, approved.Version, "retire")
	if retired.Status != "retired" {
		t.Fatal(retired)
	}
	if _, e := os.Stat(filepath.Join(f.rootPath, old)); !os.IsNotExist(e) {
		t.Fatal("old file still active", e)
	}
	if n := intentCount(t, f.pool, "SELECT count(*) FROM library_file_unit WHERE unit_id=$1 AND retired_at IS NULL", f.unit); n != 1 {
		t.Fatal("unique active mapping", n)
	}
	if n := intentCount(t, f.pool, "SELECT count(*) FROM download_task WHERE operation_id=$1 AND state='completed'", r.OperationID); n != 1 {
		t.Fatal("dedicated control projection missing or incomplete", n)
	}
}
func TestTVEpisodeUpgradePermissionVersionRollbackPG(t *testing.T) {
	for _, kind := range []string{"permission", "version", "shared-file", "rollback"} {
		t.Run(kind, func(t *testing.T) {
			f := newTVUpgradeFixture(t)
			p, e := f.service.Preview(t.Context(), f.actor, f.request)
			if e != nil {
				t.Fatal(e)
			}
			switch kind {
			case "permission":
				execIntent(t, f.pool, "UPDATE app_user SET role='member' WHERE id=$1", f.actor.ID)
			case "version":
				execIntent(t, f.pool, "UPDATE subscription SET intent_version=intent_version+1 WHERE id=$1", f.sub)
			case "shared-file":
				other := uuid.New()
				var mediaID uuid.UUID
				if e = f.pool.QueryRow(t.Context(), "SELECT media_id FROM media_unit WHERE id=$1", f.unit).Scan(&mediaID); e != nil {
					t.Fatal(e)
				}
				execIntent(t, f.pool, "INSERT INTO media_unit(id,media_id,unit_key,season_number,episode_number) VALUES($1,$2,'S02E04',2,4)", other, mediaID)
				execIntent(t, f.pool, "INSERT INTO library_file_unit(file_id,unit_id) VALUES($1,$2)", f.request.CurrentFileID, other)
			case "rollback":
				execIntent(t, f.pool, `CREATE FUNCTION mp22_fail() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'owned rollback probe'; END $$`)
				execIntent(t, f.pool, `CREATE TRIGGER mp22_fail BEFORE INSERT ON outbox_event FOR EACH ROW EXECUTE FUNCTION mp22_fail()`)
			}
			_, e = f.service.Command(t.Context(), f.actor, ports.TVEpisodeUpgradeCommand{TVEpisodeUpgradePreviewRequest: f.request, Action: "reserve", ExpectedVersion: p.Version, Confirmed: true}, "blocked")
			if e == nil {
				t.Fatal("stale or unauthorized reserve passed")
			}
			if n := intentCount(t, f.pool, "SELECT count(*) FROM tv_episode_upgrade_authority"); n != 0 {
				t.Fatal("reserve failed to roll back", n)
			}
		})
	}
}
func TestTVEpisodeUpgradeCancelRetainsPermanentPermitPG(t *testing.T) {
	f := newTVUpgradeFixture(t)
	r := f.prepared(t)
	f.native.started = make(chan struct{})
	f.native.unblock = make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, e := f.service.Command(ctx, f.actor, ports.TVEpisodeUpgradeCommand{TVEpisodeUpgradePreviewRequest: f.request, Action: "send", OperationID: r.OperationID, ExpectedVersion: r.Version, Confirmed: true}, "cancel-send")
		done <- e
	}()
	select {
	case <-f.native.started:
	case <-time.After(10 * time.Second):
		t.Fatal("send not started")
	}
	cancel()
	select {
	case <-done:
		t.Fatal("returned before physical call")
	default:
	}
	_, e := f.service.Command(t.Context(), f.actor, ports.TVEpisodeUpgradeCommand{TVEpisodeUpgradePreviewRequest: f.request, Action: "send", OperationID: r.OperationID, Confirmed: true}, "other-send")
	if e == nil {
		t.Fatal("concurrent send accepted")
	}
	close(f.native.unblock)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("physical call not drained")
	}
	_, e = f.service.Command(t.Context(), f.actor, ports.TVEpisodeUpgradeCommand{TVEpisodeUpgradePreviewRequest: f.request, Action: "send", OperationID: r.OperationID, Confirmed: true}, "new-send")
	if e == nil || f.native.calls.Load() != 1 {
		t.Fatal("cancel replay", e, f.native.calls.Load())
	}
}
func TestTVEpisodeUpgradeOrdinaryAdmissionDeniedPG(t *testing.T) {
	f := newTVUpgradeFixture(t)
	r := f.reserved(t)
	if e := postgres.RejectTVEpisodeUpgradeOperation(t.Context(), f.pool, r.OperationID); !errors.Is(e, ports.ErrTVEpisodeUpgradeObservation) {
		t.Fatal(e)
	}
	manager := download.NewManager(f.pool, idgen.New(), bytes.Repeat([]byte{27}, 32))
	if _, e := manager.PrepareDownload(t.Context(), r.OperationID, f.client, f.request.CandidateID, f.rootPath); !errors.Is(e, ports.ErrTVEpisodeUpgradeObservation) {
		t.Fatal("ordinary prepare", e)
	}
	if _, e := manager.ReconcileTask(t.Context(), r.OperationID); !errors.Is(e, ports.ErrTVEpisodeUpgradeObservation) {
		t.Fatal("ordinary reconcile", e)
	}
	if _, e := manager.ObserveTask(t.Context(), r.OperationID); !errors.Is(e, ports.ErrTVEpisodeUpgradeObservation) {
		t.Fatal("ordinary observe", e)
	}
}
