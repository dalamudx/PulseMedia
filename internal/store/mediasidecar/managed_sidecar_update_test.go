package mediasidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/metadatasidecar"
	"github.com/google/uuid"
)

type managedUpdateProvider struct {
	overview string
	poster   []byte
}

func (p *managedUpdateProvider) GetMovieDetails(context.Context, int64, string) (*ports.MovieMetadata, error) {
	return &ports.MovieMetadata{TMDBID: 6211, MediaType: "movie", Overview: p.overview, PosterURL: "https://image.tmdb.org/t/p/w500/Trusted_09-poster.jpg"}, nil
}
func (*managedUpdateProvider) GetTVDetails(context.Context, int64, string) (*ports.TVDetails, error) {
	return nil, ports.ErrRejected
}
func (p *managedUpdateProvider) FetchPoster(context.Context, string) ([]byte, error) {
	return bytes.Clone(p.poster), nil
}
func managedUpdateJPEG(t *testing.T, v uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, color.RGBA{R: v, G: 20, B: 90, A: 255})
		}
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, nil); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

type managedUpdateFixture struct {
	fixture
	actor                  *ports.Actor
	files                  *rootfs.Filesystem
	provider               *managedUpdateProvider
	managed                *ManagedUpdateRepository
	service                *metadatasidecar.ManagedUpdateService
	path, directory, video string
	videoID                ports.FileIdentity
}

func newManagedUpdateFixture(t *testing.T, legacy bool) managedUpdateFixture {
	t.Helper()
	f := newFixture(t, "movie")
	base := os.Getenv("PULSE_TEST_TEMP_ROOT")
	if base == "" {
		base = "/data/temp"
	}
	path, err := os.MkdirTemp(base, "mp29-files-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(path); err != nil {
			t.Error(err)
		}
	})
	directory := naming.FormatMovieDir("电影 & <test>", 2024)
	if err = os.MkdirAll(filepath.Join(path, directory), 0755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string][]byte{".pulsemedia_root": []byte("owned test root"), filepath.Join(directory, "source.mkv"): bytes.Repeat([]byte("video remains unchanged\x00"), 256)} {
		if err = os.WriteFile(filepath.Join(path, name), content, 0644); err != nil {
			t.Fatal(err)
		}
	}
	files := rootfs.NewFilesystem()
	root, err := files.OpenRoot(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	rootID, err := root.Identity(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	rel := filepath.Join(directory, "source.mkv")
	videoID, err := root.Inspect(t.Context(), rel)
	if err != nil {
		t.Fatal(err)
	}
	root.Close()
	// Move the synthetic placeholder into a new generation; its prior baseline stays immutable.
	f.exec(t, `UPDATE library_root SET path=$2,generation=generation+1 WHERE id=$1`, f.root, path)
	f.exec(t, `UPDATE library_root SET baseline_completed=true WHERE id=$1`, f.root)
	f.run = uuid.New()
	f.exec(t, `INSERT INTO scan_run(id,root_id,scan_token,generation,start_mutation_seq,state,finished_at) SELECT $1,id,2,generation,mutation_seq,'succeeded',clock_timestamp() FROM library_root WHERE id=$2`, f.run, f.root)
	f.exec(t, `INSERT INTO scan_finalization(scan_run_id,root_id,expected_path,expected_media_type,expected_marker_filename,expected_health_revision,root_device,root_inode,exhausted,completed_at) SELECT $1,id,path,media_type,marker_filename,health_revision,$3,$4,true,clock_timestamp() FROM library_root WHERE id=$2`, f.run, f.root, rootID.Device, rootID.Inode)
	f.exec(t, `UPDATE library_file SET rel_path=$2,size_bytes=$3,mtime_ns=$4,file_fingerprint=$5 WHERE id=$1`, f.file, rel, videoID.Size, videoID.Mtime, fmt.Sprintf("stat.v1:%d:%d:%d", videoID.Inode, videoID.Size, videoID.Mtime))
	// The file-change trigger revokes the placeholder fact; prepare a fresh actual-file import fact.
	f.exec(t, `INSERT INTO unit_fact(id,unit_id,root_id,source_file_id,stage) VALUES($1,$2,$3,$4,'imported')`, uuid.New(), f.unit, f.root, f.file)
	a := pgtest.CreateUserActor(t, db.New(f.pool), &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin})
	p := &managedUpdateProvider{overview: "Original provider overview", poster: managedUpdateJPEG(t, 40)}
	if legacy {
		for _, name := range []string{"movie.nfo", "poster.jpg"} {
			if err = os.WriteFile(filepath.Join(path, directory, name), []byte("manual legacy "+name), 0640); err != nil {
				t.Fatal(err)
			}
		}
	}
	loaded, loadErr := f.repo.Load(t.Context(), f.lease)
	if loadErr != nil {
		var diagnostic string
		_ = f.pool.QueryRow(t.Context(), `SELECT jsonb_build_object('root',to_jsonb(r),'file',to_jsonb(f),'facts',(SELECT jsonb_agg(to_jsonb(uf)) FROM unit_fact uf WHERE uf.source_file_id=f.id))::text FROM library_file f JOIN library_root r ON r.id=f.root_id WHERE f.id=$1`, f.file).Scan(&diagnostic)
		t.Fatalf("ordinary Load: %v %s", loadErr, diagnostic)
	}
	if err = metadatasidecar.NewService(f.repo, files, p, p).Execute(t.Context(), f.lease); err != nil {
		t.Fatalf("ordinary Execute: %v snapshot=%+v", err, loaded)
	}
	r := NewManagedUpdateRepository(f.pool, testIDs{})
	return managedUpdateFixture{fixture: f, actor: a, files: files, provider: p, managed: r, service: metadatasidecar.NewManagedUpdateService(r, files, p, p, testIDs{}), path: path, directory: directory, video: rel, videoID: videoID}
}
func (f managedUpdateFixture) read(t *testing.T, name string) []byte {
	t.Helper()
	b, e := os.ReadFile(filepath.Join(f.path, f.directory, name))
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func (f managedUpdateFixture) changed(t *testing.T) ports.ManagedSidecarUpdateResult {
	t.Helper()
	f.provider.overview = "Changed provider overview & <safe>"
	f.provider.poster = managedUpdateJPEG(t, 210)
	out, e := f.service.Preview(t.Context(), f.actor, f.file, f.unit)
	if e != nil || out.State != "preview" || len(out.Artifacts) != 2 {
		t.Fatalf("preview: %+v %v", out, e)
	}
	for _, a := range out.Artifacts {
		if a.State != "ready" || a.NewHash == a.OldHash {
			t.Fatalf("not ready: %+v", a)
		}
	}
	return out
}
func (f managedUpdateFixture) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if e := f.pool.QueryRow(t.Context(), sql, args...).Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n
}
func (f managedUpdateFixture) assertVideo(t *testing.T) {
	t.Helper()
	root, e := f.files.OpenRoot(t.Context(), f.path)
	if e != nil {
		t.Fatal(e)
	}
	defer root.Close()
	id, e := root.Inspect(t.Context(), f.video)
	if e != nil || id != f.videoID {
		t.Fatalf("video identity changed: %+v %v", id, e)
	}
	b, e := os.ReadFile(filepath.Join(f.path, f.video))
	if e != nil || !bytes.Equal(b, bytes.Repeat([]byte("video remains unchanged\x00"), 256)) {
		t.Fatal("video bytes changed", e)
	}
}

func TestManagedSidecarUpdateOrdinaryProvenanceAndExplicitConfirmationPG(t *testing.T) {
	f := newManagedUpdateFixture(t, false)
	if n := f.count(t, `SELECT count(*) FROM managed_sidecar_content WHERE source_job_id=$1`, f.lease.ID); n != 2 {
		t.Fatalf("ordinary provenance count=%d", n)
	}
	oldNFO, oldPoster := f.read(t, "movie.nfo"), f.read(t, "poster.jpg")
	preview := f.changed(t)
	if !bytes.Equal(oldNFO, f.read(t, "movie.nfo")) || !bytes.Equal(oldPoster, f.read(t, "poster.jpg")) {
		t.Fatal("preview performed a write")
	}
	out, e := f.service.Command(t.Context(), f.actor, f.file, preview.ID, "mp29-confirm")
	if e != nil || out.State != "completed" {
		t.Fatalf("confirm: %+v %v", out, e)
	}
	if !bytes.Contains(f.read(t, "movie.nfo"), []byte("Changed provider overview &amp; &lt;safe&gt;")) || !bytes.Equal(f.read(t, "poster.jpg"), f.provider.poster) {
		t.Fatal("provider changes not published")
	}
	var permit uuid.UUID
	var released bool
	if e = f.pool.QueryRow(t.Context(), `SELECT permit,released_at IS NOT NULL FROM managed_sidecar_update WHERE id=$1`, preview.ID).Scan(&permit, &released); e != nil || permit == uuid.Nil || !released {
		t.Fatal("permit/release", e)
	}
	for name, b := range map[string][]byte{"movie.nfo": oldNFO, "poster.jpg": oldPoster} {
		backup := fmt.Sprintf(".pulsemedia-update-%s-%s.bak", permit, name)
		if !bytes.Equal(b, f.read(t, backup)) {
			t.Fatal("backup changed", name)
		}
	}
	if n := f.count(t, `SELECT count(*) FROM managed_sidecar_content`); n != 4 {
		t.Fatalf("generation ledger=%d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM audit_log WHERE action='media.sidecar.updated'`); n != 1 {
		t.Fatalf("update audit=%d", n)
	}
	before := f.graph(t, false)
	receipt, e := f.service.ReadCommand(t.Context(), f.actor, f.file, "mp29-confirm")
	if e != nil || !reflect.DeepEqual(receipt, out) {
		t.Fatal("lost receipt recovery", receipt, e)
	}
	replay, e := f.service.Command(t.Context(), f.actor, f.file, preview.ID, "mp29-confirm")
	if e != nil || !reflect.DeepEqual(replay, out) || before != f.graph(t, false) {
		t.Fatal("original key replay mutated graph", replay, e)
	}
	f.assertVideo(t)
}

func TestManagedSidecarUpdateManualLegacyAndEditedContentHeldPG(t *testing.T) {
	for _, legacy := range []bool{true, false} {
		t.Run(fmt.Sprintf("legacy_%t", legacy), func(t *testing.T) {
			f := newManagedUpdateFixture(t, legacy)
			if !legacy {
				for _, name := range []string{"movie.nfo", "poster.jpg"} {
					if e := os.WriteFile(filepath.Join(f.path, f.directory, name), []byte("manual edited "+name), 0640); e != nil {
						t.Fatal(e)
					}
				}
			}
			oldNFO, oldPoster := f.read(t, "movie.nfo"), f.read(t, "poster.jpg")
			f.provider.overview = "new metadata"
			f.provider.poster = managedUpdateJPEG(t, 220)
			out, e := f.service.Preview(t.Context(), f.actor, f.file, f.unit)
			if e != nil || out.State != "held" {
				t.Fatal("manual file not held", out, e)
			}
			for _, a := range out.Artifacts {
				if a.State != "held" {
					t.Fatal(a)
				}
			}
			if _, e = f.service.Command(t.Context(), f.actor, f.file, out.ID, "mp29-held"); e == nil {
				t.Fatal("held preview authorized")
			}
			if !bytes.Equal(oldNFO, f.read(t, "movie.nfo")) || !bytes.Equal(oldPoster, f.read(t, "poster.jpg")) {
				t.Fatal("manual files changed")
			}
			want := 2
			if legacy {
				want = 0
			}
			if n := f.count(t, `SELECT count(*) FROM managed_sidecar_content`); n != want {
				t.Fatalf("manual provenance=%d want=%d", n, want)
			}
			f.assertVideo(t)
		})
	}
}

func TestManagedSidecarUpdateCurrentAuthorityFencesPG(t *testing.T) {
	cases := map[string]string{
		"file_version":     `UPDATE library_file SET last_mutation_seq=last_mutation_seq+1`,
		"file_retired":     `UPDATE library_file SET retired_at=clock_timestamp()`,
		"mapping":          `UPDATE library_file SET mapping_revision=mapping_revision+1`,
		"mapping_retired":  `UPDATE library_file_unit SET retired_at=clock_timestamp()`,
		"root_generation":  `UPDATE library_root SET generation=generation+1`,
		"root_identity":    `UPDATE scan_finalization SET root_inode=root_inode+1`,
		"root_offline":     `UPDATE library_root SET health='offline'`,
		"metadata":         `UPDATE media SET metadata_version=metadata_version+1`,
		"configuration":    `INSERT INTO setting(key,value) VALUES('mp29-test','{"changed":true}')`,
		"account_disabled": `UPDATE app_user SET is_active=false`,
		"account_role":     `UPDATE app_user SET role='member'`,
	}
	for name, sql := range cases {
		t.Run(name, func(t *testing.T) {
			f := newManagedUpdateFixture(t, false)
			p := f.changed(t)
			old := f.read(t, "movie.nfo")
			f.exec(t, sql)
			before := f.graph(t, false)
			if _, e := f.service.Command(t.Context(), f.actor, f.file, p.ID, "mp29-stale"); e == nil {
				t.Fatal("stale authorization accepted")
			}
			if before != f.graph(t, false) || !bytes.Equal(old, f.read(t, "movie.nfo")) {
				t.Fatal("rejected command mutated state")
			}
			if n := f.count(t, `SELECT count(*) FROM managed_sidecar_update WHERE permit IS NOT NULL`); n != 0 {
				t.Fatal("rejected permit persisted")
			}
			f.assertVideo(t)
		})
	}
}

func TestManagedSidecarUpdateFrozenTicketAndCompletionEvidencePG(t *testing.T) {
	f := newManagedUpdateFixture(t, false)
	p := f.changed(t)
	ticket, out, e := f.managed.AuthorizeManagedUpdate(t.Context(), f.actor, f.file, p.ID, "mp29-frozen")
	if e != nil {
		t.Fatal(e)
	}
	if e = f.managed.ValidateManagedUpdate(t.Context(), f.actor, ticket); e != nil {
		t.Fatal("original ticket invalid", e)
	}
	raw, e := json.Marshal(ticket)
	if e != nil {
		t.Fatal(e)
	}
	before := f.graph(t, false)
	out.State = "completed"
	for name, change := range map[string]func(*ports.ManagedSidecarUpdateTicket){
		"source_job":    func(v *ports.ManagedSidecarUpdateTicket) { v.SourceJobID = uuid.New() },
		"configuration": func(v *ports.ManagedSidecarUpdateTicket) { v.ConfigBinding = "forged" },
		"expires":       func(v *ports.ManagedSidecarUpdateTicket) { v.ExpiresAt = v.ExpiresAt.Add(time.Hour) },
		"permit":        func(v *ports.ManagedSidecarUpdateTicket) { v.Permit = uuid.New() },
		"proposed":      func(v *ports.ManagedSidecarUpdateTicket) { v.Proposed[0].Content = []byte("forged") },
		"old_content":   func(v *ports.ManagedSidecarUpdateTicket) { v.Managed[0].Content = []byte("forged") },
	} {
		t.Run(name, func(t *testing.T) {
			var altered ports.ManagedSidecarUpdateTicket
			if e := json.Unmarshal(raw, &altered); e != nil {
				t.Fatal(e)
			}
			change(&altered)
			if e := f.managed.ValidateManagedUpdate(t.Context(), f.actor, altered); e == nil {
				t.Fatal("forged ticket validated")
			}
			if _, e := f.managed.CompleteManagedUpdate(t.Context(), f.actor, altered, out, nil); e == nil {
				t.Fatal("forged ticket completed")
			}
			if before != f.graph(t, false) {
				t.Fatal("forged ticket changed graph")
			}
		})
	}
	for name, content := range map[string]ports.ManagedSidecarContent{
		"forged_content": {Name: "movie.nfo", Directory: f.directory, Content: []byte("forged"), Hash: ticket.Proposed[0].Hash, Identity: ports.FileIdentity{Device: f.videoID.Device, Inode: f.videoID.Inode + 1, Size: 6}},
		"video_identity": {Name: ticket.Proposed[0].Name, Directory: ticket.Proposed[0].Directory, Content: ticket.Proposed[0].Content, Hash: ticket.Proposed[0].Hash, Identity: f.videoID},
	} {
		t.Run(name, func(t *testing.T) {
			if _, e := f.managed.CompleteManagedUpdate(t.Context(), f.actor, ticket, out, []ports.ManagedSidecarContent{content}); e == nil {
				t.Fatal("forged completion accepted")
			}
			if before != f.graph(t, false) {
				t.Fatal("forged completion changed graph")
			}
		})
	}
	f.assertVideo(t)
}

func TestManagedSidecarUpdateTruncateCannotEraseAuthorityPG(t *testing.T) {
	f := newManagedUpdateFixture(t, false)
	p := f.changed(t)
	if _, _, e := f.managed.AuthorizeManagedUpdate(t.Context(), f.actor, f.file, p.ID, "mp29-truncate"); e != nil {
		t.Fatal(e)
	}
	for _, table := range []string{"managed_sidecar_content", "managed_sidecar_update"} {
		tx, e := f.pool.Begin(t.Context())
		if e != nil {
			t.Fatal(e)
		}
		_, e = tx.Exec(t.Context(), "TRUNCATE "+table)
		rollbackErr := tx.Rollback(t.Context())
		if rollbackErr != nil {
			t.Fatal(rollbackErr)
		}
		if e == nil {
			t.Errorf("TRUNCATE can erase permanent authority: %s", table)
		}
	}
}

func TestManagedSidecarUpdatePermitAndLedgerImmutablePG(t *testing.T) {
	f := newManagedUpdateFixture(t, false)
	p := f.changed(t)
	ticket, _, e := f.managed.AuthorizeManagedUpdate(t.Context(), f.actor, f.file, p.ID, "mp29-permanent")
	if e != nil || ticket.Permit == uuid.Nil {
		t.Fatal(e)
	}
	for _, sql := range []string{
		`UPDATE managed_sidecar_update SET permit=gen_random_uuid()`,
		`UPDATE managed_sidecar_update SET permit=NULL,command_key=NULL`,
		`UPDATE managed_sidecar_update SET command_key='rebound'`,
		`UPDATE managed_sidecar_update SET lease_until=clock_timestamp()+interval '1 hour'`,
		`UPDATE managed_sidecar_update SET ticket='{}'`,
		`DELETE FROM managed_sidecar_update`,
		`UPDATE managed_sidecar_content SET content=content`,
		`DELETE FROM managed_sidecar_content`,
	} {
		if _, e = f.pool.Exec(t.Context(), sql); e == nil {
			t.Fatal("immutable evidence accepted", sql)
		}
	}
	repeated, receipt, e := f.managed.AuthorizeManagedUpdate(t.Context(), f.actor, f.file, p.ID, "mp29-permanent")
	if e != nil || repeated.Permit != uuid.Nil || receipt.State != "review_required" {
		t.Fatal("permit replay", repeated, receipt, e)
	}
}

type managedUpdateFaultFiles struct {
	ports.ManagedSidecarUpdateFilesystem
	replace func(context.Context, ports.TransferRoot, ports.SidecarRequest, ports.ManagedSidecarContent, uuid.UUID) (ports.SidecarResult, error)
}

func (f managedUpdateFaultFiles) ReplaceManagedSidecar(c context.Context, r ports.TransferRoot, q ports.SidecarRequest, o ports.ManagedSidecarContent, p uuid.UUID) (ports.SidecarResult, error) {
	return f.replace(c, r, q, o, p)
}

func TestManagedSidecarUpdateUnknownRetainsPhysicalRowPG(t *testing.T) {
	f := newManagedUpdateFixture(t, false)
	p := f.changed(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var calls atomic.Int32
	files := managedUpdateFaultFiles{ManagedSidecarUpdateFilesystem: f.files, replace: func(c context.Context, r ports.TransferRoot, q ports.SidecarRequest, o ports.ManagedSidecarContent, p uuid.UUID) (ports.SidecarResult, error) {
		calls.Add(1)
		result, e := f.files.ReplaceManagedSidecar(c, r, q, o, p)
		cancel()
		return result, errors.Join(e, context.Canceled)
	}}
	s := metadatasidecar.NewManagedUpdateService(f.managed, files, f.provider, f.provider, testIDs{})
	if _, e := s.Command(ctx, f.actor, f.file, p.ID, "mp29-unknown"); !errors.Is(e, context.Canceled) {
		t.Fatal("expected canceled physical outcome", e)
	}
	var state string
	var occupied bool
	if e := f.pool.QueryRow(t.Context(), `SELECT state,permit IS NOT NULL AND released_at IS NULL FROM managed_sidecar_update WHERE id=$1`, p.ID).Scan(&state, &occupied); e != nil || state != "executing" || !occupied {
		t.Fatal("unknown lost physical row", state, occupied, e)
	}
	receipt, e := s.ReadCommand(t.Context(), f.actor, f.file, "mp29-unknown")
	if e != nil || receipt.State != "review_required" {
		t.Fatal(receipt, e)
	}
	if _, e = s.Command(t.Context(), f.actor, f.file, p.ID, "mp29-unknown"); e != nil || calls.Load() != 1 {
		t.Fatal("unknown command replayed", calls.Load(), e)
	}
	next, e := f.service.Preview(t.Context(), f.actor, f.file, f.unit)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.Command(t.Context(), f.actor, f.file, next.ID, "mp29-second"); e == nil {
		t.Fatal("unknown physical permit replaced")
	}
	if calls.Load() != 1 || f.count(t, `SELECT count(*) FROM managed_sidecar_update WHERE permit IS NOT NULL AND released_at IS NULL`) != 1 {
		t.Fatal("unknown exclusivity lost")
	}
	f.assertVideo(t)
}

func TestManagedSidecarUpdateUncertainPublicationRetainsPhysicalRowPG(t *testing.T) {
	f := newManagedUpdateFixture(t, false)
	p := f.changed(t)
	var calls atomic.Int32
	files := managedUpdateFaultFiles{ManagedSidecarUpdateFilesystem: f.files, replace: func(c context.Context, r ports.TransferRoot, q ports.SidecarRequest, o ports.ManagedSidecarContent, p uuid.UUID) (ports.SidecarResult, error) {
		calls.Add(1)
		result, e := f.files.ReplaceManagedSidecar(c, r, q, o, p)
		if e != nil {
			return result, e
		}
		result.Disposition = ports.SidecarUncertain
		return result, errors.New("injected lost physical completion")
	}}
	s := metadatasidecar.NewManagedUpdateService(f.managed, files, f.provider, f.provider, testIDs{})
	out, e := s.Command(t.Context(), f.actor, f.file, p.ID, "mp29-uncertain")
	if e == nil && out.State != "review_required" {
		t.Fatal("uncertain publication reported success", out)
	}
	if n := f.count(t, `SELECT count(*) FROM managed_sidecar_update WHERE id=$1 AND permit IS NOT NULL AND released_at IS NULL`, p.ID); n != 1 {
		t.Fatal("uncertain publication released physical row", n)
	}
	if _, e = s.Command(t.Context(), f.actor, f.file, p.ID, "mp29-uncertain"); e != nil || calls.Load() != 1 {
		t.Fatal("uncertain publication replayed", calls.Load(), e)
	}
	if n := f.count(t, `SELECT count(*) FROM managed_sidecar_content`); n != 2 {
		t.Fatal("uncertain publication claimed provenance", n)
	}
	f.assertVideo(t)
}

func TestManagedSidecarUpdateChangesDuringPhysicalWritePG(t *testing.T) {
	for name, sql := range map[string]string{
		"account":       `UPDATE app_user SET is_active=false`,
		"configuration": `INSERT INTO setting(key,value) VALUES('mp29-during-write','true')`,
		"mapping":       `UPDATE library_file SET mapping_revision=mapping_revision+1`,
		"root":          `UPDATE library_root SET health_revision=health_revision+1`,
		"file":          `UPDATE library_file SET last_mutation_seq=last_mutation_seq+1`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newManagedUpdateFixture(t, false)
			p := f.changed(t)
			var calls int
			files := managedUpdateFaultFiles{ManagedSidecarUpdateFilesystem: f.files, replace: func(c context.Context, r ports.TransferRoot, q ports.SidecarRequest, o ports.ManagedSidecarContent, p uuid.UUID) (ports.SidecarResult, error) {
				calls++
				result, e := f.files.ReplaceManagedSidecar(c, r, q, o, p)
				if e == nil {
					f.exec(t, sql)
				}
				return result, e
			}}
			s := metadatasidecar.NewManagedUpdateService(f.managed, files, f.provider, f.provider, testIDs{})
			if _, e := s.Command(t.Context(), f.actor, f.file, p.ID, "mp29-during-write"); e == nil {
				t.Fatal("changed authority accepted completion")
			}
			if calls != 1 {
				t.Fatal("remaining physical writes continued", calls)
			}
			if n := f.count(t, `SELECT count(*) FROM managed_sidecar_content`); n != 2 {
				t.Fatal("changed authority committed provenance", n)
			}
			if n := f.count(t, `SELECT count(*) FROM managed_sidecar_update WHERE id=$1 AND permit IS NOT NULL AND released_at IS NULL`, p.ID); n != 1 {
				t.Fatal("changed authority released physical row", n)
			}
			f.assertVideo(t)
		})
	}
}

func TestManagedSidecarUpdateCompletionRollbackAndOriginalKeyPG(t *testing.T) {
	f := newManagedUpdateFixture(t, false)
	p := f.changed(t)
	f.exec(t, `CREATE FUNCTION mp29_fail_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='media.sidecar.updated' THEN RAISE EXCEPTION 'MP29 deferred commit failure'; END IF; RETURN NEW; END $$`)
	f.exec(t, `CREATE CONSTRAINT TRIGGER mp29_fail_commit AFTER INSERT ON audit_log DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION mp29_fail_commit()`)
	if _, e := f.service.Command(t.Context(), f.actor, f.file, p.ID, "mp29-rollback"); e == nil {
		t.Fatal("faulted commit succeeded")
	}
	if n := f.count(t, `SELECT count(*) FROM managed_sidecar_content`); n != 2 {
		t.Fatal("generation escaped rollback", n)
	}
	if n := f.count(t, `SELECT count(*) FROM audit_log WHERE action='media.sidecar.updated'`); n != 0 {
		t.Fatal("audit escaped rollback", n)
	}
	if !bytes.Contains(f.read(t, "movie.nfo"), []byte("Changed provider overview")) {
		t.Fatal("test did not reach physical publication")
	}
	before := f.graph(t, false)
	nfo, poster := f.read(t, "movie.nfo"), f.read(t, "poster.jpg")
	receipt, e := f.service.ReadCommand(t.Context(), f.actor, f.file, "mp29-rollback")
	if e != nil || receipt.State != "review_required" {
		t.Fatal(receipt, e)
	}
	if _, e = f.service.Command(t.Context(), f.actor, f.file, p.ID, "mp29-rollback"); e != nil {
		t.Fatal(e)
	}
	if before != f.graph(t, false) || !bytes.Equal(nfo, f.read(t, "movie.nfo")) || !bytes.Equal(poster, f.read(t, "poster.jpg")) {
		t.Fatal("rollback receipt replayed filesystem")
	}
	f.assertVideo(t)
}

func TestManagedSidecarUpdateExpiryWhileWaitingForRealRowLockPG(t *testing.T) {
	f := newManagedUpdateFixture(t, false)
	p := f.changed(t)
	// Insert a second genuine preview with a short lifetime; immutable evidence is never rewritten.
	var ticket, result []byte
	if e := f.pool.QueryRow(t.Context(), `SELECT ticket,result FROM managed_sidecar_update WHERE id=$1`, p.ID).Scan(&ticket, &result); e != nil {
		t.Fatal(e)
	}
	var decoded ports.ManagedSidecarUpdateTicket
	if e := json.Unmarshal(ticket, &decoded); e != nil {
		t.Fatal(e)
	}
	id := uuid.New()
	decoded.ID = id
	decoded.ExpiresAt = time.Now().Add(700 * time.Millisecond)
	ticket, e := json.Marshal(decoded)
	if e != nil {
		t.Fatal(e)
	}
	f.exec(t, `INSERT INTO managed_sidecar_update(id,owner_id,file_id,ticket,result,expires_at,state) VALUES($1,$2,$3,$4,$5,clock_timestamp()+interval '700 milliseconds','preview')`, id, uuid.MustParse(f.actor.ID), f.file, ticket, result)
	tx, e := f.pool.Begin(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, e = tx.Exec(t.Context(), `SELECT id FROM managed_sidecar_update WHERE id=$1 FOR UPDATE`, id); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := f.managed.AuthorizeManagedUpdate(t.Context(), f.actor, f.file, id, "mp29-expired-lock")
		done <- err
	}()
	// PostgreSQL owns the delay, while another transaction owns the preview row lock.
	if _, e = tx.Exec(t.Context(), `SELECT pg_sleep(1)`); e != nil {
		t.Fatal(e)
	}
	select {
	case e := <-done:
		t.Fatalf("authorization did not wait for row lock: %v", e)
	default:
	}
	if e = tx.Commit(t.Context()); e != nil {
		t.Fatal(e)
	}
	select {
	case e = <-done:
		if e == nil {
			t.Fatal("expired waiter acquired permit")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("lock waiter did not terminate")
	}
	if n := f.count(t, `SELECT count(*) FROM managed_sidecar_update WHERE permit IS NOT NULL`); n != 0 {
		t.Fatal("expiry left permit", n)
	}
	f.assertVideo(t)
}

func TestManagedSidecarUpdatePhysicalNamespaceChangesPG(t *testing.T) {
	for _, kind := range []string{"source_inode", "root_inode", "sidecar_after_preview"} {
		t.Run(kind, func(t *testing.T) {
			f := newManagedUpdateFixture(t, false)
			defer f.assertVideo(t)
			p := f.changed(t)
			oldNFO := f.read(t, "movie.nfo")
			switch kind {
			case "source_inode":
				target := filepath.Join(f.path, f.video)
				content, e := os.ReadFile(target)
				if e != nil {
					t.Fatal(e)
				}
				if e = os.Rename(target, target+".original"); e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(target, content, 0644); e != nil {
					t.Fatal(e)
				}
				defer func() {
					if e := os.Remove(target); e != nil {
						t.Error(e)
					}
					if e := os.Rename(target+".original", target); e != nil {
						t.Error(e)
					}
				}()
			case "root_inode":
				if e := os.Rename(f.path, f.path+".original"); e != nil {
					t.Fatal(e)
				}
				if e := os.Mkdir(f.path, 0755); e != nil {
					t.Fatal(e)
				}
				defer func() {
					if e := os.RemoveAll(f.path); e != nil {
						t.Error(e)
					}
					if e := os.Rename(f.path+".original", f.path); e != nil {
						t.Error(e)
					}
				}()
			case "sidecar_after_preview":
				if e := os.WriteFile(filepath.Join(f.path, f.directory, "movie.nfo"), []byte("edited after preview"), 0644); e != nil {
					t.Fatal(e)
				}
			}
			out, e := f.service.Command(t.Context(), f.actor, f.file, p.ID, "mp29-physical-change")
			if e == nil && out.State != "review_required" {
				t.Fatal("changed physical namespace accepted", out)
			}
			if kind == "source_inode" && !bytes.Equal(oldNFO, f.read(t, "movie.nfo")) {
				t.Fatal("source replacement authorized NFO update")
			}
			if kind == "sidecar_after_preview" && string(f.read(t, "movie.nfo")) != "edited after preview" {
				t.Fatal("edited NFO overwritten")
			}
			if n := f.count(t, `SELECT count(*) FROM managed_sidecar_content`); n != 2 {
				t.Fatal("changed namespace added generation", n)
			}
		})
	}
}

func TestManagedSidecarUpdateExpiryAfterAuditLockPG(t *testing.T) {
	f := newManagedUpdateFixture(t, false)
	p := f.changed(t)
	var ticket, result []byte
	if e := f.pool.QueryRow(t.Context(), `SELECT ticket,result FROM managed_sidecar_update WHERE id=$1`, p.ID).Scan(&ticket, &result); e != nil {
		t.Fatal(e)
	}
	var decoded ports.ManagedSidecarUpdateTicket
	if e := json.Unmarshal(ticket, &decoded); e != nil {
		t.Fatal(e)
	}
	decoded.ID = uuid.New()
	decoded.ExpiresAt = time.Now().Add(2 * time.Second)
	ticket, e := json.Marshal(decoded)
	if e != nil {
		t.Fatal(e)
	}
	f.exec(t, `INSERT INTO managed_sidecar_update(id,owner_id,file_id,ticket,result,expires_at,state) VALUES($1,$2,$3,$4,$5,clock_timestamp()+interval '2 seconds','preview')`, decoded.ID, uuid.MustParse(f.actor.ID), f.file, ticket, result)
	// The real audit write blocks after Complete's first clock check, before its final guard.
	f.exec(t, `CREATE FUNCTION mp29_delay_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='media.sidecar.updated' THEN PERFORM pg_sleep(2.1); END IF; RETURN NEW; END $$`)
	f.exec(t, `CREATE TRIGGER mp29_delay_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION mp29_delay_audit()`)
	if _, e = f.service.Command(t.Context(), f.actor, f.file, decoded.ID, "mp29-expired-tail"); e == nil {
		t.Fatal("expired completion committed")
	}
	if n := f.count(t, `SELECT count(*) FROM managed_sidecar_content`); n != 2 {
		t.Fatal("expired transaction retained generation", n)
	}
	if n := f.count(t, `SELECT count(*) FROM audit_log WHERE action='media.sidecar.updated'`); n != 0 {
		t.Fatal("expired transaction retained success audit", n)
	}
	if n := f.count(t, `SELECT count(*) FROM managed_sidecar_update WHERE id=$1 AND permit IS NOT NULL AND released_at IS NULL`, decoded.ID); n != 1 {
		t.Fatal("expired physical outcome released", n)
	}
	f.assertVideo(t)
}

func TestManagedSidecarUpdateRepeatedContentGenerationPG(t *testing.T) {
	f := newManagedUpdateFixture(t, false)
	originalOverview := f.provider.overview
	originalPoster := bytes.Clone(f.provider.poster)
	p := f.changed(t)
	if out, e := f.service.Command(t.Context(), f.actor, f.file, p.ID, "mp29-generation-b"); e != nil || out.State != "completed" {
		t.Fatal(out, e)
	}
	f.provider.overview = originalOverview
	f.provider.poster = originalPoster
	p, e := f.service.Preview(t.Context(), f.actor, f.file, f.unit)
	if e != nil || p.State != "preview" {
		t.Fatal(p, e)
	}
	out, e := f.service.Command(t.Context(), f.actor, f.file, p.ID, "mp29-generation-a")
	if e != nil || out.State != "completed" {
		t.Fatal("A to B to A generation must append new provenance", out, e)
	}
	if n := f.count(t, `SELECT count(*) FROM managed_sidecar_content`); n != 6 {
		t.Fatal("generation history lost", n)
	}
	if !strings.Contains(string(f.read(t, "movie.nfo")), originalOverview) {
		t.Fatal("original metadata not restored")
	}
	f.assertVideo(t)
}
