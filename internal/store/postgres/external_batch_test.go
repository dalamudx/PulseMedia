package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
	"sync"
	"testing"
)

type externalBatchFixture struct {
	f     *untrackedReviewFixture
	mid   uuid.UUID
	store *postgres.ExternalBatchStore
	input ports.ExternalBatchInput
}

func newExternalBatchFixture(t *testing.T, variant string) *externalBatchFixture {
	t.Helper()
	f, mid := canonicalReviewFixture(t)
	relative := "external/two.mkv"
	if variant == "directory" {
		relative = "other/two.mkv"
	}
	f.request(t)
	capture := f.claim(t)
	identity := ports.FileIdentity{Device: 2, Inode: 334, Size: 101, Mtime: 201}
	if variant == "inode" {
		identity = discoverySample()[0].Identity
	}
	observations := append(discoverySample(), ports.UntrackedFileObservation{RelativePath: relative, Identity: identity})
	if err := f.discovery.RecordUntrackedDiscovery(t.Context(), capture, observations); err != nil {
		t.Fatal(err)
	}
	if err := f.discovery.CompleteUntrackedDiscovery(t.Context(), capture); err != nil {
		t.Fatal(err)
	}
	var second ports.UntrackedDiscoveredFile
	for _, file := range f.files(t) {
		if file.RelativePath == relative {
			second = file
		}
	}
	if second.ID == uuid.Nil {
		t.Fatal("missing second observed file")
	}
	f2 := *f
	f2.file = second
	unit2 := uuid.New()
	season := 1
	tmdb := int64(123)
	secondMid := mid
	if variant == "season" {
		season = 2
	}
	if variant == "work" {
		secondMid = uuid.New()
		tmdb = 124
		f.exec(t, `INSERT INTO media(id,title,original_title,year,media_type) VALUES($1,'Other','Other',2020,'tv')`, secondMid)
		f.exec(t, `INSERT INTO media_identity(id,media_id,namespace,external_id) VALUES($1,$2,'tmdb.tv','124')`, uuid.New(), secondMid)
	}
	if variant != "unit" {
		f.exec(t, `INSERT INTO media_unit(id,media_id,unit_key,season_number,episode_number) VALUES($1,$2,$3,$4,8)`, unit2, secondMid, fmt.Sprintf("S%02dE08", season), season)
		f.exec(t, `INSERT INTO media_unit_identity(unit_id,namespace,external_id,tmdb_series_id) VALUES($1,'tmdb.episode',1008,$2)`, unit2, tmdb)
		f2.input.CanonicalUnitID = unit2.String()
		f2.input.TMDBID = tmdb
		f2.input.Season = season
		f2.input.Episode = 8
		f2.input.TMDBEpisodeID = 1008
	}
	f2.input.FileRevision = second.Revision
	inputs := ports.ExternalBatchInput{}
	for i, row := range []*untrackedReviewFixture{f, &f2} {
		request := row.requestReview(t, fmt.Sprintf("batch-review-%d", i))
		c := row.claimReview(t)
		e := canonicalReviewEvidence()
		e.ParserVersion = "untracked-review.v1/" + media.EpisodeParserVersion
		e.Candidate.TMDBID = row.input.TMDBID
		e.Candidate.Key = fmt.Sprintf("tmdb.tv:%d", row.input.TMDBID)
		e.Candidate.Units[0] = ports.UntrackedReviewUnit{UnitKey: fmt.Sprintf("S%02dE%02d", row.input.Season, row.input.Episode), Season: row.input.Season, Episode: row.input.Episode, TMDBEpisodeID: row.input.TMDBEpisodeID}
		if variant == "ambiguous" && i == 1 {
			e.Complete = false
			e.Reason = "identity_ambiguous"
			e.Candidate = nil
		}
		if err := row.review.CompleteUntrackedReview(t.Context(), c, e); err != nil {
			t.Fatal(err)
		}
		v, err := row.review.ReadUntrackedReview(t.Context(), row.a, row.scope, row.file.ID, request.Request.ID)
		if err != nil {
			t.Fatal(err)
		}
		in := ports.UntrackedConfirmationInput{ReviewID: v.Request.ID, MaterialHash: v.MaterialHash, ScopeVersion: 1, FileRevision: row.file.Revision, CandidateKey: fmt.Sprintf("tmdb.tv:%d", row.input.TMDBID), Action: "hardlink", TargetRelativePath: fmt.Sprintf("Shows/Series/Season %02d/Series.S%02dE%02d.mkv", row.input.Season, row.input.Season, row.input.Episode)}
		inputs.Rows = append(inputs.Rows, ports.ExternalBatchSelection{FileID: row.file.ID, Confirmation: in})
	}
	if variant == "target" {
		inputs.Rows[1].Confirmation.TargetRelativePath = inputs.Rows[0].Confirmation.TargetRelativePath
	}
	if variant == "unit" {
		inputs.Rows[1].Confirmation.TargetRelativePath = "Shows/Series/Season 01/Series.duplicate.mkv"
	}
	return &externalBatchFixture{f: f, mid: mid, store: postgres.NewExternalBatchStore(f.p, idgen.New(), hintClock{}), input: inputs}
}
func (f *externalBatchFixture) create(t *testing.T, key string) *ports.ExternalBatch {
	t.Helper()
	b, e := f.store.CreateExternalBatch(t.Context(), f.f.a, f.f.scope, f.input, key)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func TestExternalBatchCanonicalManifestAndConcurrentRecoveryPG(t *testing.T) {
	f := newExternalBatchFixture(t, "")
	ctx := t.Context()
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	ids := make(chan uuid.UUID, 4)
	for range 4 {
		wg.Go(func() {
			b, e := f.store.CreateExternalBatch(ctx, f.f.a, f.f.scope, f.input, "same-key")
			errs <- e
			if b != nil {
				ids <- b.ID
			}
		})
	}
	wg.Wait()
	close(errs)
	close(ids)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var id uuid.UUID
	for got := range ids {
		if id == uuid.Nil {
			id = got
		}
		if got != id {
			t.Fatal("original key changed batch")
		}
	}
	f.f.count(t, `SELECT count(*) FROM external_import_batch`, 1)
	f.f.count(t, `SELECT count(*) FROM untracked_review_confirmation`, 0)
	f.f.count(t, `SELECT count(*) FROM transfer_plan`, 0)
	f.f.count(t, `SELECT count(*) FROM audit_log WHERE action='external.batch.previewed'`, 1)
	original, e := f.store.ReadExternalBatch(ctx, f.f.a, f.f.scope, id, "")
	if e != nil {
		t.Fatal(e)
	}
	f.f.exec(t, `UPDATE media SET metadata_version=metadata_version+1 WHERE id=$1`, f.mid)
	recovered, e := f.store.ReadExternalBatch(ctx, f.f.a, f.f.scope, uuid.Nil, "same-key")
	if e != nil || !sameHistoricalJSON(original, recovered) {
		t.Fatal("history changed", e)
	}
	duplicate, e := f.store.CreateExternalBatch(ctx, f.f.a, f.f.scope, f.input, "same-key")
	if e != nil || !sameHistoricalJSON(original, duplicate) {
		t.Fatal("same-key recovery revalidated current metadata", e)
	}
	changed := f.input
	changed.Rows = append([]ports.ExternalBatchSelection{}, f.input.Rows...)
	changed.Rows[0].Confirmation.TargetRelativePath = "different.mkv"
	_, e = f.store.CreateExternalBatch(ctx, f.f.a, f.f.scope, changed, "same-key")
	checkinErr(t, e, ports.ErrEndpointVersionConflict)
	_, e = f.f.p.Exec(ctx, `UPDATE external_import_batch SET command_key='rewrite'`)
	if e == nil {
		t.Fatal("manifest mutable")
	}
}
func TestExternalBatchRejectsDuplicateAndAmbiguousSelectionPG(t *testing.T) {
	for _, variant := range []string{"unit", "target", "directory", "work", "season", "inode", "ambiguous"} {
		t.Run(variant, func(t *testing.T) {
			f := newExternalBatchFixture(t, variant)
			_, e := f.store.CreateExternalBatch(t.Context(), f.f.a, f.f.scope, f.input, "invalid")
			if e == nil {
				t.Fatal("invalid batch accepted")
			}
			f.f.count(t, `SELECT count(*) FROM external_import_batch`, 0)
			f.f.count(t, `SELECT count(*) FROM untracked_review_confirmation`, 0)
		})
	}
	f := newExternalBatchFixture(t, "")
	duplicate := f.input
	duplicate.Rows = append([]ports.ExternalBatchSelection{}, f.input.Rows...)
	duplicate.Rows[1] = duplicate.Rows[0]
	if _, e := f.store.CreateExternalBatch(t.Context(), f.f.a, f.f.scope, duplicate, "duplicate-file"); e == nil {
		t.Fatal("duplicate file")
	}
	for _, n := range []int{0, 21} {
		in := ports.ExternalBatchInput{Rows: make([]ports.ExternalBatchSelection, n)}
		if _, e := f.store.CreateExternalBatch(t.Context(), f.f.a, f.f.scope, in, "bound"); e == nil {
			t.Fatal("unbounded batch")
		}
	}
}
func TestExternalBatchIndependentFrozenConfirmationAndOrganizationPG(t *testing.T) {
	f := newExternalBatchFixture(t, "")
	b := f.create(t, "independent")
	row := b.Rows[0]
	prep, e := f.f.review.PrepareUntrackedConfirmation(t.Context(), f.f.a, f.f.scope, row.File.ID, row.Selection.Confirmation, "first-confirm")
	if e != nil {
		t.Fatal(e)
	}
	receipt, e := f.f.review.CommitUntrackedConfirmation(t.Context(), prep.Capture)
	if e != nil {
		t.Fatal(e)
	}
	store := postgres.NewUntrackedOrganizationStore(f.f.p, idgen.New(), hintClock{})
	org, e := store.PrepareUntrackedOrganization(t.Context(), f.f.a, f.f.scope, row.File.ID, ports.UntrackedOrganizationInput{ConfirmationID: receipt.Material.ID}, "first-organize")
	if e != nil {
		t.Fatal(e)
	}
	queued, e := store.CommitUntrackedOrganization(t.Context(), org.Capture)
	if e != nil {
		t.Fatal(e)
	}
	second := b.Rows[1]
	f.f.exec(t, `UPDATE media_unit SET metadata_version=metadata_version+1 WHERE id=$1`, uuid.MustParse(second.Review.Request.Input.CanonicalUnitID))
	_, e = f.f.review.PrepareUntrackedConfirmation(t.Context(), f.f.a, f.f.scope, second.File.ID, second.Selection.Confirmation, "second-confirm")
	if !errors.Is(e, ports.ErrUntrackedReviewStale) {
		t.Fatal("stale row re-signed", e)
	}
	historical, e := store.ReadUntrackedOrganizationReceipt(t.Context(), f.f.a, f.f.scope, row.File.ID, "first-organize")
	if e != nil || !sameHistoricalJSON(historical, queued) {
		t.Fatal("first row receipt changed", e)
	}
	f.f.count(t, `SELECT count(*) FROM untracked_review_confirmation`, 1)
	f.f.count(t, `SELECT count(*) FROM untracked_organization_operation`, 1)
	after, e := f.store.ReadExternalBatch(t.Context(), f.f.a, f.f.scope, b.ID, "")
	if e != nil || after.Rows[1].Review.Request.Input.UnitMetadataVersion != second.Review.Request.Input.UnitMetadataVersion {
		t.Fatal("manifest re-signed stale row", e)
	}
}
func TestExternalBatchCommitTailAndCurrentHistoryPermissionPG(t *testing.T) {
	for _, variant := range []string{"media", "file", "account"} {
		t.Run(variant, func(t *testing.T) {
			f := newExternalBatchFixture(t, "")
			var sql string
			switch variant {
			case "media":
				sql = `UPDATE media SET metadata_version=metadata_version+1 WHERE id='` + f.mid.String() + `'`
			case "file":
				sql = `UPDATE untracked_discovered_file SET revision=revision+1 WHERE id='` + f.input.Rows[1].FileID.String() + `'`
			case "account":
				sql = `UPDATE app_user SET is_active=false WHERE id='` + f.f.a.ID + `'`
			}
			f.f.exec(t, `CREATE FUNCTION batch_tail_change() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+sql+`;RETURN NULL;END $$;CREATE CONSTRAINT TRIGGER batch_tail_change AFTER INSERT ON external_import_batch DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION batch_tail_change()`)
			if _, e := f.store.CreateExternalBatch(t.Context(), f.f.a, f.f.scope, f.input, "tail"); e == nil {
				t.Fatal("tail change committed")
			}
			f.f.count(t, `SELECT count(*) FROM external_import_batch`, 0)
		})
	}
	f := newExternalBatchFixture(t, "")
	b := f.create(t, "permission")
	f.f.exec(t, `UPDATE app_user SET role='member' WHERE id=$1`, uuid.MustParse(f.f.a.ID))
	if _, e := f.store.ReadExternalBatch(context.Background(), f.f.a, f.f.scope, b.ID, ""); e == nil {
		t.Fatal("revoked administrator read history")
	}
}
