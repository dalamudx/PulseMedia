package library

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type untrackedReviewTestCapture struct{ snapshot ports.UntrackedReviewSnapshot }

func (c *untrackedReviewTestCapture) UntrackedReviewSnapshot() ports.UntrackedReviewSnapshot {
	return c.snapshot
}

type untrackedReviewTestConfirmationCapture struct {
	material ports.UntrackedConfirmationMaterial
}

func (c *untrackedReviewTestConfirmationCapture) UntrackedConfirmationMaterial() ports.UntrackedConfirmationMaterial {
	return c.material
}

type untrackedReviewTestRepo struct {
	request                                                             *ports.UntrackedReviewRequestReceipt
	review                                                              *ports.UntrackedReview
	preparation                                                         *ports.UntrackedConfirmationPreparation
	confirmation                                                        *ports.UntrackedConfirmationReceipt
	capture                                                             *untrackedReviewTestConfirmationCapture
	err                                                                 error
	requests, reads, requestReads, prepares, commits, confirmationReads int
	nilCommit                                                           bool
	beforePrepare                                                       func()
	onCommit                                                            func(*ports.UntrackedConfirmationReceipt)
}

func (r *untrackedReviewTestRepo) RequestUntrackedReview(_ context.Context, _ *ports.Actor, _, _ uuid.UUID, _ ports.UntrackedReviewInput, _ string) (*ports.UntrackedReviewRequestReceipt, error) {
	r.requests++
	return r.request, r.err
}
func (r *untrackedReviewTestRepo) ReadUntrackedReviewRequestReceipt(_ context.Context, _ *ports.Actor, _, _ uuid.UUID, _ string) (*ports.UntrackedReviewRequestReceipt, error) {
	r.requestReads++
	return r.request, r.err
}
func (r *untrackedReviewTestRepo) ReadUntrackedReview(_ context.Context, _ *ports.Actor, _, _, _ uuid.UUID) (*ports.UntrackedReview, error) {
	r.reads++
	return r.review, r.err
}
func (r *untrackedReviewTestRepo) PrepareUntrackedConfirmation(_ context.Context, _ *ports.Actor, _, _ uuid.UUID, _ ports.UntrackedConfirmationInput, _ string) (*ports.UntrackedConfirmationPreparation, error) {
	r.prepares++
	if r.beforePrepare != nil {
		r.beforePrepare()
	}
	return r.preparation, r.err
}
func (r *untrackedReviewTestRepo) CommitUntrackedConfirmation(_ context.Context, c ports.UntrackedConfirmationCapture) (*ports.UntrackedConfirmationReceipt, error) {
	r.commits++
	if c != r.capture {
		return nil, errors.New("replaced opaque capture")
	}
	if r.err != nil {
		return nil, r.err
	}
	if r.nilCommit {
		return nil, nil
	}
	out := &ports.UntrackedConfirmationReceipt{CommandKey: "confirm-1", Material: untrackedReviewMaterialCopy(r.capture.material), State: "confirmed_pending_organization"}
	if r.onCommit != nil {
		r.onCommit(out)
	}
	return out, nil
}
func (r *untrackedReviewTestRepo) ReadUntrackedConfirmationReceipt(_ context.Context, _ *ports.Actor, _, _ uuid.UUID, _ string) (*ports.UntrackedConfirmationReceipt, error) {
	r.confirmationReads++
	return r.confirmation, r.err
}

type untrackedReviewTestExecutionRepo struct {
	capture                     *untrackedReviewTestCapture
	loads, validates, completes int
	evidence                    ports.UntrackedReviewEvidence
	err                         error
	onValidate                  func(int)
}

func (r *untrackedReviewTestExecutionRepo) LoadUntrackedReview(context.Context, ports.JobLease) (ports.UntrackedReviewCapture, error) {
	r.loads++
	if r.capture == nil {
		return nil, r.err
	}
	return r.capture, r.err
}
func (r *untrackedReviewTestExecutionRepo) ValidateUntrackedReview(_ context.Context, c ports.UntrackedReviewCapture) error {
	r.validates++
	if c != r.capture {
		return errors.New("replaced opaque capture")
	}
	if r.onValidate != nil {
		r.onValidate(r.validates)
	}
	return r.err
}
func (r *untrackedReviewTestExecutionRepo) CompleteUntrackedReview(_ context.Context, c ports.UntrackedReviewCapture, e ports.UntrackedReviewEvidence) error {
	if c != r.capture {
		return errors.New("replaced opaque capture")
	}
	r.completes++
	r.evidence = e
	return r.err
}
func (*untrackedReviewTestExecutionRepo) RecoverUntrackedReviews(context.Context, int) (int, error) {
	return 0, nil
}

type untrackedReviewTestFS struct {
	identities                            map[string]ports.FileIdentity
	source, target, relative, marker      string
	file, markerIdentity                  ports.FileIdentity
	opens, closes, locks, releases, slots atomic.Int32
	inspect                               func(context.Context, string, string)
	openError                             bool
	lockOrder                             []ports.FileIdentity
}
type untrackedReviewTestRoot struct {
	fs       *untrackedReviewTestFS
	path     string
	identity ports.FileIdentity
	closed   bool
}

func (r *untrackedReviewTestRoot) Identity(context.Context) (ports.FileIdentity, error) {
	if r.closed {
		return ports.FileIdentity{}, errors.New("closed capability")
	}
	return r.identity, nil
}
func (r *untrackedReviewTestRoot) Inspect(ctx context.Context, relative string) (ports.FileIdentity, error) {
	if r.closed {
		return ports.FileIdentity{}, errors.New("closed capability")
	}
	if r.fs.inspect != nil {
		r.fs.inspect(ctx, r.path, relative)
	}
	if r.path == r.fs.target && relative == r.fs.marker {
		return r.fs.markerIdentity, nil
	}
	if r.path == r.fs.source && relative == r.fs.relative {
		return r.fs.file, nil
	}
	return ports.FileIdentity{}, ports.ErrFileIdentity
}
func (r *untrackedReviewTestRoot) Close() {
	if !r.closed {
		r.closed = true
		r.fs.closes.Add(1)
	}
}
func (f *untrackedReviewTestFS) OpenRoot(_ context.Context, path string) (ports.TransferRoot, error) {
	i, ok := f.identities[path]
	if !ok {
		return nil, errors.New("private absolute path")
	}
	f.opens.Add(1)
	root := &untrackedReviewTestRoot{fs: f, path: path, identity: i}
	if f.openError {
		return root, errors.New("private open failure")
	}
	return root, nil
}
func (f *untrackedReviewTestFS) LockRoots(ctx context.Context, paths ...string) (map[string]ports.TransferRoot, func(), error) {
	f.locks.Add(1)
	roots, unique := map[string]ports.TransferRoot{}, map[ports.FileIdentity]bool{}
	for _, path := range paths {
		if roots[path] != nil {
			continue
		}
		root, err := f.OpenRoot(ctx, path)
		if err != nil {
			return roots, func() {
				for _, r := range roots {
					r.Close()
				}
			}, err
		}
		roots[path] = root
		unique[untrackedScopePhysical(f.identities[path])] = true
	}
	for identity := range unique {
		f.lockOrder = append(f.lockOrder, identity)
	}
	sort.Slice(f.lockOrder, func(i, j int) bool {
		a, b := f.lockOrder[i], f.lockOrder[j]
		return a.Device < b.Device || (a.Device == b.Device && a.Inode < b.Inode)
	})
	f.slots.Add(int32(len(unique)))
	return roots, func() {
		for _, root := range roots {
			root.Close()
		}
		f.slots.Add(-int32(len(unique)))
		f.releases.Add(1)
	}, nil
}

type untrackedReviewTestMetadata struct {
	search                         ports.MovieIdentitySearch
	movie                          *ports.MovieMetadata
	tv                             *ports.TVDetails
	season                         *ports.TVSeasonMetadata
	searches, movies, tvs, seasons int
	err                            error
	onCall                         func(context.Context, string)
	query, kind                    string
	year                           int
	id                             int64
}

func (m *untrackedReviewTestMetadata) call(ctx context.Context, op string) {
	if m.onCall != nil {
		m.onCall(ctx, op)
	}
}
func (m *untrackedReviewTestMetadata) SearchMediaIdentityCandidates(ctx context.Context, query, kind string, year int) (ports.MovieIdentitySearch, error) {
	m.searches++
	m.query, m.kind, m.year = query, kind, year
	m.call(ctx, "search")
	return m.search, m.err
}
func (m *untrackedReviewTestMetadata) GetMovieDetails(ctx context.Context, id int64, _ string) (*ports.MovieMetadata, error) {
	m.movies++
	m.id = id
	m.call(ctx, "movie")
	return m.movie, m.err
}
func (m *untrackedReviewTestMetadata) GetTVDetails(ctx context.Context, id int64, _ string) (*ports.TVDetails, error) {
	m.tvs++
	m.id = id
	m.call(ctx, "tv")
	return m.tv, m.err
}
func (m *untrackedReviewTestMetadata) GetTVSeasonMetadata(ctx context.Context, id int64, _ int, _ string) (*ports.TVSeasonMetadata, error) {
	m.seasons++
	m.id = id
	m.call(ctx, "season")
	return m.season, m.err
}

type untrackedReviewTestFixture struct {
	actor     *ports.Actor
	snapshot  ports.UntrackedReviewSnapshot
	service   *UntrackedReviewService
	runner    *UntrackedReviewRunner
	repo      *untrackedReviewTestRepo
	execution *untrackedReviewTestExecutionRepo
	files     *untrackedReviewTestFS
	metadata  *untrackedReviewTestMetadata
	input     ports.UntrackedConfirmationInput
}

func untrackedReviewFixture() *untrackedReviewTestFixture {
	at := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	owner, scopeID, fileID, reviewID, jobID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	scope := ports.UntrackedScope{ID: scopeID, OwnerID: owner, Enabled: true, Version: 1, ClientID: uuid.New(), ClientVersion: at, ClientStamp: "configured-v1", RemotePrefix: "/downloads", LocalRoot: "/configured/source", SourceIdentity: ports.FileIdentity{Device: 6, Inode: 20}, TargetRootID: uuid.New(), TargetRootPath: "/configured/target", TargetRootGeneration: 1, TargetMediaType: "movie", TargetMarker: ".pulsemedia_root", TargetBaselineScanID: uuid.New(), TargetIdentity: ports.FileIdentity{Device: 5, Inode: 10}, CreatedAt: at, UpdatedAt: at}
	file := ports.UntrackedDiscoveredFile{ID: fileID, ScopeID: scopeID, ScopeVersion: 1, Revision: 2, RelativePath: "Example.2020.1080p.mkv", Identity: ports.FileIdentity{Device: 6, Inode: 30, Size: 100, Mtime: 99}, FirstObservedAt: at, LastObservedAt: at, Observations: 2}
	request := ports.UntrackedReviewRequest{ID: reviewID, ScopeID: scopeID, FileID: fileID, ActorID: owner, Input: ports.UntrackedReviewInput{ScopeVersion: 1, FileRevision: 2, MediaType: "movie"}, JobID: jobID, CreatedAt: at}
	candidate := &ports.UntrackedReviewCandidate{Key: "tmdb.movie:42", Namespace: "tmdb.movie", TMDBID: 42, Title: "Example", OriginalTitle: "Example", Year: 2020, Units: []ports.UntrackedReviewUnit{{UnitKey: "movie"}}}
	review := ports.UntrackedReview{Request: request, State: "ready", Evidence: &ports.UntrackedReviewEvidence{ParserVersion: untrackedReviewParserVersion, ParsedTitle: "Example", ParsedYear: 2020, Selection: "automatic", Complete: true, Reason: "ready", Candidate: candidate}, MaterialHash: strings.Repeat("a", 64), RecordedAt: &at}
	input := ports.UntrackedConfirmationInput{ReviewID: reviewID, MaterialHash: review.MaterialHash, ScopeVersion: 1, FileRevision: 2, CandidateKey: candidate.Key, Action: "hardlink", TargetRelativePath: "Example (2020)/Example.mkv"}
	capture := &untrackedReviewTestConfirmationCapture{material: ports.UntrackedConfirmationMaterial{ID: uuid.New(), ActorID: owner, Scope: scope, File: file, Review: review, Input: input, CreatedAt: at}}
	repo := &untrackedReviewTestRepo{capture: capture, request: &ports.UntrackedReviewRequestReceipt{CommandKey: "request-1", Request: request}, review: &review, preparation: &ports.UntrackedConfirmationPreparation{Capture: capture}}
	repo.confirmation = &ports.UntrackedConfirmationReceipt{CommandKey: "confirm-1", Material: untrackedReviewMaterialCopy(capture.material), State: "confirmed_pending_organization"}
	lease := ports.JobLease{ID: jobID, Owner: "review-worker", Token: 1}
	snapshot := ports.UntrackedReviewSnapshot{Lease: lease, Request: request, Scope: scope, File: file}
	execution := &untrackedReviewTestExecutionRepo{capture: &untrackedReviewTestCapture{snapshot: snapshot}}
	files := &untrackedReviewTestFS{source: scope.LocalRoot, target: scope.TargetRootPath, relative: file.RelativePath, marker: scope.TargetMarker, file: file.Identity, markerIdentity: ports.FileIdentity{Device: 5, Inode: 999, Size: 4, Mtime: 1}, identities: map[string]ports.FileIdentity{scope.LocalRoot: scope.SourceIdentity, scope.TargetRootPath: scope.TargetIdentity}}
	details := &ports.MovieMetadata{TMDBID: 42, MediaType: "movie", Title: "Example", OriginalTitle: "Example", Year: 2020}
	search := *details
	metadata := &untrackedReviewTestMetadata{movie: details, search: ports.MovieIdentitySearch{Complete: true, Candidates: []*ports.MovieMetadata{&search}}}
	return &untrackedReviewTestFixture{actor: &ports.Actor{ID: owner.String(), Type: ports.ActorTypeUser, Role: ports.RoleAdmin}, snapshot: snapshot, repo: repo, execution: execution, files: files, metadata: metadata, input: input, service: NewUntrackedReviewService(repo, files), runner: NewUntrackedReviewRunner(execution, files, metadata, metadata)}
}
func (f *untrackedReviewTestFixture) run(ctx context.Context) error {
	return f.runner.Run(ctx, f.snapshot.Lease)
}
func (f *untrackedReviewTestFixture) confirm(ctx context.Context) (*ports.UntrackedConfirmationReceipt, error) {
	return f.service.Confirm(ctx, f.actor, f.snapshot.Scope.ID, f.snapshot.File.ID, f.input, "confirm-1")
}
func (f *untrackedReviewTestFixture) path(relative string) {
	f.execution.capture.snapshot.File.RelativePath = relative
	f.files.relative = relative
}
func (f *untrackedReviewTestFixture) tv() {
	s := &f.execution.capture.snapshot
	s.Scope.TargetMediaType, s.Request.Input.MediaType = "tv", "tv"
	f.path("Example.2020.S01E01E03.1080p.mkv")
	f.metadata.search.Candidates[0].MediaType = "tv"
	f.metadata.tv = &ports.TVDetails{TMDBID: 42, Title: "Example", OriginalTitle: "Example", Year: 2020, SeasonsCount: 1, EpisodesCount: 2, Seasons: []ports.TVSeasonDetails{{SeasonNumber: 1, EpisodeCount: 2}}}
	a, b := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2020, 1, 15, 0, 0, 0, 0, time.UTC)
	f.metadata.season = &ports.TVSeasonMetadata{SeasonNumber: 1, Complete: true, Episodes: []ports.TVEpisodeMetadata{{EpisodeNumber: 3, TMDBEpisodeID: 403, AirDate: &b}, {EpisodeNumber: 1, TMDBEpisodeID: 401, AirDate: &a}}}
}
func untrackedReviewAssertClosed(t *testing.T, f *untrackedReviewTestFS) {
	t.Helper()
	if f.opens.Load() != f.closes.Load() || f.slots.Load() != 0 {
		t.Fatalf("capability leak: opened=%d closed=%d slots=%d", f.opens.Load(), f.closes.Load(), f.slots.Load())
	}
}

func TestUntrackedReviewMovieEvidenceAndNoPublication(t *testing.T) {
	f := untrackedReviewFixture()
	f.metadata.search.Candidates = append(f.metadata.search.Candidates, f.metadata.search.Candidates[0])
	f.metadata.onCall = func(ctx context.Context, _ string) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 55*time.Second {
			t.Error("unbounded provider context")
		}
		if f.files.slots.Load() != 2 {
			t.Error("metadata ran without both physical slots")
		}
	}
	if err := f.run(t.Context()); err != nil {
		t.Fatal(err)
	}
	e := f.execution.evidence
	if f.execution.completes != 1 || !e.Complete || e.Reason != "ready" || e.Selection != "automatic" || e.ParserVersion != "untracked-review.v1/"+media.EpisodeParserVersion || e.Candidate == nil || e.Candidate.Key != "tmdb.movie:42" || len(e.Candidate.Units) != 1 || e.Candidate.Units[0] != (ports.UntrackedReviewUnit{UnitKey: "movie"}) {
		t.Fatalf("unexpected evidence: %+v", e)
	}
	if f.metadata.searches != 1 || f.metadata.movies != 1 || f.metadata.tvs != 0 || f.metadata.query != "Example" || f.metadata.kind != "movie" || f.metadata.year != 2020 {
		t.Fatal("identity search or detail reread changed")
	}
	if len(f.files.lockOrder) != 2 || f.files.lockOrder[0].Device != 5 || f.files.releases.Load() != 1 {
		t.Fatal("physical root ownership changed")
	}
	untrackedReviewAssertClosed(t, f.files)
}

func TestUntrackedReviewTVSparseCoverage(t *testing.T) {
	for _, path := range []string{"Example.2020.S01E01E03.1080p.mkv", "Example.2020.S01E03.1080p.mkv"} {
		t.Run(path, func(t *testing.T) {
			f := untrackedReviewFixture()
			f.tv()
			f.path(path)
			if err := f.run(t.Context()); err != nil {
				t.Fatal(err)
			}
			e := f.execution.evidence
			if !e.Complete || e.Candidate == nil || e.Candidate.Key != "tmdb.tv:42" || f.metadata.tvs != 1 || f.metadata.movies != 0 || f.metadata.seasons != 1 {
				t.Fatalf("TV unresolved: %+v", e)
			}
			units := e.Candidate.Units
			if units[len(units)-1].Episode != 3 || units[len(units)-1].TMDBEpisodeID != 403 || units[len(units)-1].UnitKey != "S01E03" {
				t.Fatal("sparse episode identity lost")
			}
			if strings.Contains(path, "E01E03") && (len(units) != 2 || units[0].Episode != 1) {
				t.Fatal("coverage changed")
			}
			if !strings.Contains(path, "E01E03") && len(units) != 1 {
				t.Fatal("selected unparsed episodes")
			}
			untrackedReviewAssertClosed(t, f.files)
		})
	}
}

func TestUntrackedReviewAutomaticUnresolved(t *testing.T) {
	for _, name := range []string{"ambiguous", "partial", "empty", "unknown-year", "parent-conflict", "special", "absolute", "partial-content", "detail-id", "detail-kind", "detail-year", "detail-title", "malformed-row", "tag-is-not-selection"} {
		t.Run(name, func(t *testing.T) {
			f := untrackedReviewFixture()
			reason, noNetwork := "", false
			switch name {
			case "ambiguous":
				other := *f.metadata.search.Candidates[0]
				other.TMDBID = 43
				f.metadata.search.Candidates = append(f.metadata.search.Candidates, &other)
				reason = "identity_ambiguous"
			case "partial":
				f.metadata.search.Complete = false
				reason = "identity_incomplete"
			case "empty":
				f.metadata.search.Candidates = nil
				reason = "identity_unresolved"
			case "unknown-year":
				f.path("Example.1080p.mkv")
				noNetwork = true
			case "parent-conflict":
				f.path("Other.S02E01/Example.2020.1080p.mkv")
				noNetwork = true
			case "special":
				f.tv()
				f.path("Example.2020.S00E01.mkv")
				noNetwork = true
			case "absolute":
				f.tv()
				f.path("[Group] Example - 03 [1080p].mkv")
				noNetwork = true
			case "partial-content":
				f.path("Example.2020.sample.mkv")
				noNetwork = true
			case "detail-id":
				f.metadata.movie.TMDBID++
				reason = "detail_mismatch"
			case "detail-kind":
				f.metadata.movie.MediaType = "tv"
				reason = "detail_mismatch"
			case "detail-year":
				f.metadata.movie.Year++
				reason = "detail_mismatch"
			case "detail-title":
				f.metadata.movie.Title = "Other"
				reason = "detail_mismatch"
			case "malformed-row":
				f.metadata.search.Candidates = append(f.metadata.search.Candidates, nil)
				reason = "identity_incomplete"
			case "tag-is-not-selection":
				f.path("Example.2020.{tmdb-999}.mkv")
				f.metadata.search.Complete = false
				reason = "identity_incomplete"
			}
			if err := f.run(t.Context()); err != nil {
				t.Fatal(err)
			}
			e := f.execution.evidence
			if f.execution.completes != 1 || e.Complete || e.Candidate != nil || e.Selection != "automatic" || !untrackedReviewReason(e.Reason) || (reason != "" && e.Reason != reason) {
				t.Fatalf("unsafe unresolved evidence: %+v", e)
			}
			if noNetwork && f.metadata.searches+f.metadata.movies+f.metadata.tvs+f.metadata.seasons != 0 {
				t.Fatal("untrusted parse performed metadata I/O")
			}
			untrackedReviewAssertClosed(t, f.files)
		})
	}
}

func TestUntrackedReviewExplicitTypedIdentity(t *testing.T) {
	for _, kind := range []string{"movie", "tv"} {
		t.Run(kind, func(t *testing.T) {
			f := untrackedReviewFixture()
			if kind == "tv" {
				f.tv()
				f.metadata.tv.Title = "Deliberately selected TV"
			} else {
				f.metadata.movie.Title = "Deliberately selected movie"
			}
			f.execution.capture.snapshot.Request.Input.TMDBID = 42
			f.metadata.search.Complete = false
			if err := f.run(t.Context()); err != nil {
				t.Fatal(err)
			}
			e := f.execution.evidence
			if !e.Complete || e.Selection != "explicit" || e.Candidate == nil || e.Candidate.Namespace != "tmdb."+kind || f.metadata.searches != 0 || f.metadata.id != 42 {
				t.Fatalf("explicit selection was reinterpreted: %+v", e)
			}
			if kind == "tv" && (f.metadata.movies != 0 || f.metadata.tvs != 1) {
				t.Fatal("TV ID sent to movie namespace")
			}
			untrackedReviewAssertClosed(t, f.files)
		})
	}
	f := untrackedReviewFixture()
	f.tv()
	f.execution.capture.snapshot.Request.Input.TMDBID = 42
	f.path("Conflicting.S02E01/Example.2020.S01E03.mkv")
	if err := f.run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if f.execution.evidence.Complete || f.metadata.tvs != 0 {
		t.Fatal("explicit identity bypassed coverage conflict")
	}
}

func TestUntrackedReviewTVRejectsUnprovenEpisodeMetadata(t *testing.T) {
	for _, name := range []string{"count", "total", "season-count", "wrong-season", "incomplete", "missing", "duplicate-number", "duplicate-id", "zero-id", "nonmidnight-date", "invalid-date", "absolute-number", "detail-id"} {
		t.Run(name, func(t *testing.T) {
			f := untrackedReviewFixture()
			f.tv()
			switch name {
			case "count":
				f.metadata.season.Episodes = f.metadata.season.Episodes[:1]
			case "total":
				f.metadata.tv.EpisodesCount++
			case "season-count":
				f.metadata.tv.SeasonsCount++
			case "wrong-season":
				f.metadata.season.SeasonNumber = 2
			case "incomplete":
				f.metadata.season.Complete = false
			case "missing":
				f.path("Example.2020.S01E02.mkv")
			case "duplicate-number":
				f.metadata.season.Episodes[0].EpisodeNumber = 1
			case "duplicate-id":
				f.metadata.season.Episodes[0].TMDBEpisodeID = 401
			case "zero-id":
				f.metadata.season.Episodes[0].TMDBEpisodeID = 0
			case "nonmidnight-date":
				v := f.metadata.season.Episodes[0].AirDate.Add(1)
				f.metadata.season.Episodes[0].AirDate = &v
			case "invalid-date":
				v := time.Date(1700, 1, 1, 0, 0, 0, 0, time.UTC)
				f.metadata.season.Episodes[0].AirDate = &v
			case "absolute-number":
				v := 3
				f.metadata.season.Episodes[0].AbsoluteNumber = &v
			case "detail-id":
				f.metadata.tv.TMDBID++
			}
			if err := f.run(t.Context()); err != nil {
				t.Fatal(err)
			}
			if f.execution.completes != 1 || f.execution.evidence.Complete || f.execution.evidence.Candidate != nil {
				t.Fatalf("invented TV authority: %+v", f.execution.evidence)
			}
			untrackedReviewAssertClosed(t, f.files)
		})
	}
}

func TestUntrackedReviewRequestAndHistoricalReadsZeroFilesystem(t *testing.T) {
	f := untrackedReviewFixture()
	f.service.files = nil
	scopeID, fileID := f.snapshot.Scope.ID, f.snapshot.File.ID
	if _, err := f.service.Request(t.Context(), f.actor, scopeID, fileID, f.snapshot.Request.Input, "request-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.RequestReceipt(t.Context(), f.actor, scopeID, fileID, "request-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Read(t.Context(), f.actor, scopeID, fileID, f.snapshot.Request.ID); err != nil {
		t.Fatal(err)
	}
	f.repo.preparation = &ports.UntrackedConfirmationPreparation{Receipt: f.repo.confirmation}
	before, _ := json.Marshal(f.repo.confirmation)
	out, err := f.confirm(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(out)
	if !bytes.Equal(before, after) {
		t.Fatal("historical receipt was rewritten")
	}
	if _, err := f.service.ConfirmationReceipt(t.Context(), f.actor, scopeID, fileID, "confirm-1"); err != nil {
		t.Fatal(err)
	}
	if f.repo.commits != 0 || f.files.opens.Load() != 0 || f.files.locks.Load() != 0 {
		t.Fatal("history or request accessed filesystem")
	}
}

func TestUntrackedReviewConfirmationPrepareAndExactCommit(t *testing.T) {
	f := untrackedReviewFixture()
	f.repo.beforePrepare = func() {
		if f.files.opens.Load() != 0 {
			t.Error("I/O before Prepare")
		}
	}
	f.repo.onCommit = func(out *ports.UntrackedConfirmationReceipt) {
		if f.files.slots.Load() != 2 {
			t.Error("slots released before commit")
		}
		zone := time.FixedZone("same-instant", 8*60*60)
		out.Material.CreatedAt = out.Material.CreatedAt.In(zone)
		out.Material.Scope.ClientVersion = out.Material.Scope.ClientVersion.In(zone)
		out.Material.File.FirstObservedAt = out.Material.File.FirstObservedAt.In(zone)
		out.Material.Review.Request.CreatedAt = out.Material.Review.Request.CreatedAt.In(zone)
		v := out.Material.Review.RecordedAt.In(zone)
		out.Material.Review.RecordedAt = &v
	}
	out, err := f.confirm(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if out.ExecutionAllowed || out.State != "confirmed_pending_organization" || f.repo.commits != 1 || out.Material.CreatedAt.Location().String() != "same-instant" {
		t.Fatal("confirmation changed or historical time rewritten")
	}
	untrackedReviewAssertClosed(t, f.files)
}

func TestUntrackedReviewForgedConfirmationMaterialAndOutput(t *testing.T) {
	mutations := map[string]func(*ports.UntrackedConfirmationReceipt){
		"actor":  func(r *ports.UntrackedConfirmationReceipt) { r.Material.ActorID = uuid.New() },
		"scope":  func(r *ports.UntrackedConfirmationReceipt) { r.Material.Scope.ID = uuid.New() },
		"owner":  func(r *ports.UntrackedConfirmationReceipt) { r.Material.Scope.OwnerID = uuid.New() },
		"file":   func(r *ports.UntrackedConfirmationReceipt) { r.Material.File.ID = uuid.New() },
		"review": func(r *ports.UntrackedConfirmationReceipt) { r.Material.Review.Request.ID = uuid.New() },
		"hash":   func(r *ports.UntrackedConfirmationReceipt) { r.Material.Review.MaterialHash = strings.Repeat("b", 64) },
		"candidate": func(r *ports.UntrackedConfirmationReceipt) {
			r.Material.Review.Evidence.Candidate.Key = "tmdb.movie:43"
		},
		"version":    func(r *ports.UntrackedConfirmationReceipt) { r.Material.Scope.Version++ },
		"generation": func(r *ports.UntrackedConfirmationReceipt) { r.Material.Scope.TargetRootGeneration++ },
		"baseline":   func(r *ports.UntrackedConfirmationReceipt) { r.Material.Scope.TargetBaselineScanID = uuid.New() },
		"path":       func(r *ports.UntrackedConfirmationReceipt) { r.Material.File.RelativePath = "different.mkv" },
		"identity":   func(r *ports.UntrackedConfirmationReceipt) { r.Material.File.Identity.Mtime++ },
		"action":     func(r *ports.UntrackedConfirmationReceipt) { r.Material.Input.Action = "move" },
		"target":     func(r *ports.UntrackedConfirmationReceipt) { r.Material.Input.TargetRelativePath = "Other.mkv" },
		"title":      func(r *ports.UntrackedConfirmationReceipt) { r.Material.Review.Evidence.Candidate.Title = "Other" },
		"timestamp": func(r *ports.UntrackedConfirmationReceipt) {
			r.Material.CreatedAt = r.Material.CreatedAt.Add(time.Second)
		},
		"state":     func(r *ports.UntrackedConfirmationReceipt) { r.State = "published" },
		"execution": func(r *ports.UntrackedConfirmationReceipt) { r.ExecutionAllowed = true },
		"key":       func(r *ports.UntrackedConfirmationReceipt) { r.CommandKey = "different" },
	}
	for name, mutate := range mutations {
		t.Run("commit-"+name, func(t *testing.T) {
			f := untrackedReviewFixture()
			f.repo.onCommit = mutate
			if out, err := f.confirm(t.Context()); err != ports.ErrUnavailable || out != nil {
				t.Fatalf("forged commit accepted: out=%v err=%v", out, err)
			}
			if f.repo.commits != 1 || f.repo.prepares != 1 {
				t.Fatal("retried unknown commit")
			}
			untrackedReviewAssertClosed(t, f.files)
		})
	}
	for _, name := range []string{"actor", "scope", "owner", "file", "review", "hash", "candidate", "version", "action", "target"} {
		t.Run("capture-"+name, func(t *testing.T) {
			f := untrackedReviewFixture()
			r := &ports.UntrackedConfirmationReceipt{Material: untrackedReviewMaterialCopy(f.repo.capture.material)}
			mutations[name](r)
			f.repo.capture.material = r.Material
			if out, err := f.confirm(t.Context()); err != ports.ErrUnavailable || out != nil {
				t.Fatalf("forged capture accepted: %v", err)
			}
			if f.files.opens.Load() != 0 || f.repo.commits != 0 {
				t.Fatal("invalid material reached I/O")
			}
		})
	}
}

func TestUntrackedReviewRejectsInvalidInputsAndActors(t *testing.T) {
	for _, name := range []string{"nil-actor", "member", "token", "empty-scopes", "noncanonical-id", "zero-id", "file", "key", "version", "hash", "candidate", "action", "target-dot", "target-parent", "target-backslash", "review"} {
		t.Run(name, func(t *testing.T) {
			f := untrackedReviewFixture()
			key, fileID := "confirm-1", f.snapshot.File.ID
			switch name {
			case "nil-actor":
				f.actor = nil
			case "member":
				f.actor.Role = "member"
			case "token":
				f.actor.Type = ports.ActorTypeService
			case "empty-scopes":
				f.actor.Scopes = []string{}
			case "noncanonical-id":
				f.actor.ID = "{" + f.actor.ID + "}"
			case "zero-id":
				f.actor.ID = uuid.Nil.String()
			case "file":
				fileID = uuid.Nil
			case "key":
				key = "unsafe/key"
			case "version":
				f.input.ScopeVersion = 0
			case "hash":
				f.input.MaterialHash = strings.Repeat("A", 64)
			case "candidate":
				f.input.CandidateKey = "tmdb.movie:042"
			case "action":
				f.input.Action = "publish"
			case "target-dot":
				f.input.TargetRelativePath = "."
			case "target-parent":
				f.input.TargetRelativePath = "a/../b.mkv"
			case "target-backslash":
				f.input.TargetRelativePath = "a\\b.mkv"
			case "review":
				f.input.ReviewID = uuid.Nil
			}
			if _, err := f.service.Confirm(t.Context(), f.actor, f.snapshot.Scope.ID, fileID, f.input, key); err == nil {
				t.Fatal("invalid command accepted")
			}
			if f.repo.prepares != 0 || f.files.opens.Load() != 0 {
				t.Fatal("invalid command crossed boundary")
			}
		})
	}
}

func TestUntrackedReviewInvalidLeaseAndSnapshotZeroFilesystem(t *testing.T) {
	for _, name := range []string{"missing", "lease", "token", "owner", "job", "scope", "disabled", "file", "revision", "type", "path", "validation"} {
		t.Run(name, func(t *testing.T) {
			f := untrackedReviewFixture()
			s := &f.execution.capture.snapshot
			switch name {
			case "missing":
				f.execution.capture = nil
			case "lease":
				s.Lease.ID = uuid.New()
			case "token":
				s.Lease.Token++
			case "owner":
				s.Request.ActorID = uuid.New()
			case "job":
				s.Request.JobID = uuid.New()
			case "scope":
				s.Request.ScopeID = uuid.New()
			case "disabled":
				s.Scope.Enabled = false
			case "file":
				s.File.ScopeID = uuid.New()
			case "revision":
				s.Request.Input.FileRevision++
			case "type":
				s.Request.Input.MediaType = "tv"
			case "path":
				s.File.RelativePath = "../escape.mkv"
			case "validation":
				f.execution.err = errors.New("private DSN")
			}
			if err := f.run(t.Context()); err == nil {
				t.Fatal("invalid authority accepted")
			}
			if f.files.opens.Load() != 0 || f.execution.completes != 0 || f.metadata.searches != 0 {
				t.Fatal("invalid authority reached I/O")
			}
		})
	}
}

func TestUntrackedReviewPhysicalChangesAndDirectoryChurn(t *testing.T) {
	for _, stage := range []string{"before", "after"} {
		for _, name := range []string{"source", "target", "file-size", "file-mtime", "marker", "directory-churn", "capture"} {
			t.Run(stage+"-"+name, func(t *testing.T) {
				f := untrackedReviewFixture()
				change := func() {
					switch name {
					case "source":
						i := f.files.identities[f.files.source]
						i.Inode++
						f.files.identities[f.files.source] = i
					case "target":
						i := f.files.identities[f.files.target]
						i.Inode++
						f.files.identities[f.files.target] = i
					case "file-size":
						f.files.file.Size++
					case "file-mtime":
						f.files.file.Mtime++
					case "marker":
						f.files.markerIdentity.Inode++
					case "directory-churn":
						for path, i := range f.files.identities {
							i.Size++
							i.Mtime++
							f.files.identities[path] = i
						}
					case "capture":
						f.execution.capture.snapshot.Scope.ClientStamp = "changed"
					}
				}
				if stage == "before" {
					change()
				} else {
					f.metadata.onCall = func(_ context.Context, op string) {
						if op == "movie" {
							change()
						}
					}
				}
				err := f.run(t.Context())
				// A marker's initial identity is observed at execution, never supplied by the caller.
				valid := name == "directory-churn" || (name == "marker" && stage == "before") || (name == "capture" && stage == "before")
				if valid {
					if err != nil || f.execution.completes != 1 {
						t.Fatalf("valid baseline rejected: %v", err)
					}
				} else if err == nil || f.execution.completes != 0 {
					t.Fatal("stale evidence committed")
				}
				if stage == "before" && !valid && f.metadata.searches != 0 {
					t.Fatal("stale physical source reached metadata")
				}
				untrackedReviewAssertClosed(t, f.files)
			})
		}
	}
}

func TestUntrackedReviewIgnoredCancellationRetainsPhysicalOwnership(t *testing.T) {
	for _, operation := range []string{"metadata", "filesystem"} {
		t.Run(operation, func(t *testing.T) {
			f := untrackedReviewFixture()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			entered, canceled, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan error, 1)
			unblocked, joined := false, false
			defer func() {
				cancel()
				if !unblocked {
					close(release)
				}
				if !joined {
					select {
					case <-done:
					case <-time.After(2 * time.Second):
						t.Error("owned runner did not exit during cleanup")
					}
				}
			}()
			blocked := false
			block := func(ctx context.Context) {
				if blocked {
					return
				}
				blocked = true
				close(entered)
				<-ctx.Done()
				close(canceled)
				<-release
			}
			if operation == "metadata" {
				f.metadata.onCall = func(ctx context.Context, _ string) { block(ctx) }
			} else {
				f.files.inspect = func(ctx context.Context, path, relative string) {
					if path == f.files.source && relative == f.files.relative {
						block(ctx)
					}
				}
			}
			go func() { done <- f.run(ctx) }()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("I/O did not start")
			}
			cancel()
			select {
			case <-canceled:
			case <-time.After(2 * time.Second):
				t.Fatal("I/O did not observe cancellation")
			}
			if f.files.slots.Load() != 2 || f.files.releases.Load() != 0 {
				t.Error("logical cancel released physical ownership")
			}
			select {
			case err := <-done:
				joined = true
				t.Errorf("returned before actual I/O: %v", err)
			default:
			}
			close(release)
			unblocked = true
			if !joined {
				select {
				case err := <-done:
					joined = true
					if !errors.Is(err, context.Canceled) {
						t.Errorf("cancellation lost: %v", err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("did not join actual I/O")
				}
			}
			if f.execution.completes != 0 {
				t.Fatal("canceled evidence completed")
			}
			untrackedReviewAssertClosed(t, f.files)
		})
	}
}

func TestUntrackedReviewProviderAndOpenErrorsAreSafe(t *testing.T) {
	for _, operation := range []string{"metadata", "open"} {
		f := untrackedReviewFixture()
		if operation == "metadata" {
			f.metadata.err = errors.New("private token and URL")
		} else {
			f.files.openError = true
		}
		if err := f.run(t.Context()); err != ports.ErrUnavailable {
			t.Fatalf("unsafe error: %v", err)
		}
		if f.execution.completes != 0 {
			t.Fatal("failed observation completed")
		}
		untrackedReviewAssertClosed(t, f.files)
	}
}

func TestUntrackedReviewSameKeyAndSamePhysicalRoot(t *testing.T) {
	f := untrackedReviewFixture()
	f.service.files = nil
	f.repo.preparation = &ports.UntrackedConfirmationPreparation{Receipt: f.repo.confirmation}
	for i := 0; i < 2; i++ {
		if _, err := f.service.Request(t.Context(), f.actor, f.snapshot.Scope.ID, f.snapshot.File.ID, f.snapshot.Request.Input, "request-1"); err != nil {
			t.Fatal(err)
		}
		if _, err := f.confirm(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if f.repo.commits != 0 || f.files.opens.Load() != 0 {
		t.Fatal("same-key replay performed physical I/O")
	}
	f = untrackedReviewFixture()
	identity := f.execution.capture.snapshot.Scope.TargetIdentity
	f.execution.capture.snapshot.Scope.SourceIdentity = identity
	f.files.identities[f.files.source] = identity
	f.metadata.onCall = func(_ context.Context, _ string) {
		if f.files.slots.Load() != 1 {
			t.Error("aliased root was not physically deduplicated")
		}
	}
	if err := f.run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(f.files.lockOrder) != 1 || f.files.releases.Load() != 1 {
		t.Fatal("same physical root double-acquired")
	}
	untrackedReviewAssertClosed(t, f.files)
}

func TestUntrackedReviewMissingAndForgedReadResults(t *testing.T) {
	for _, operation := range []string{"request", "request-receipt", "review", "confirmation-receipt", "replay", "missing-preparation", "double-preparation", "missing-commit"} {
		for _, forged := range []bool{false, true} {
			t.Run(operation+"/"+map[bool]string{false: "missing", true: "forged"}[forged], func(t *testing.T) {
				f := untrackedReviewFixture()
				var err error
				scopeID, fileID := f.snapshot.Scope.ID, f.snapshot.File.ID
				switch operation {
				case "request", "request-receipt":
					if forged {
						f.repo.request.ExecutionAllowed = true
					} else {
						f.repo.request = nil
					}
					if operation == "request" {
						_, err = f.service.Request(t.Context(), f.actor, scopeID, fileID, f.snapshot.Request.Input, "request-1")
					} else {
						_, err = f.service.RequestReceipt(t.Context(), f.actor, scopeID, fileID, "request-1")
					}
				case "review":
					if forged {
						f.repo.review.Evidence.Candidate.Units[0].TMDBEpisodeID = 5
					} else {
						f.repo.review = nil
					}
					_, err = f.service.Read(t.Context(), f.actor, scopeID, fileID, f.snapshot.Request.ID)
				case "confirmation-receipt", "replay":
					if forged {
						f.repo.confirmation.ExecutionAllowed = true
					} else {
						f.repo.confirmation = nil
					}
					if operation == "replay" {
						f.repo.preparation = &ports.UntrackedConfirmationPreparation{Receipt: f.repo.confirmation}
						_, err = f.confirm(t.Context())
					} else {
						_, err = f.service.ConfirmationReceipt(t.Context(), f.actor, scopeID, fileID, "confirm-1")
					}
				case "missing-preparation":
					f.repo.preparation = nil
					_, err = f.confirm(t.Context())
				case "double-preparation":
					f.repo.preparation.Receipt = f.repo.confirmation
					_, err = f.confirm(t.Context())
				case "missing-commit":
					if forged {
						f.repo.onCommit = func(r *ports.UntrackedConfirmationReceipt) { *r = ports.UntrackedConfirmationReceipt{} }
					} else {
						f.repo.nilCommit = true
					}
					_, err = f.confirm(t.Context())
				}
				if err != ports.ErrUnavailable {
					t.Fatalf("unsafe result accepted: %v", err)
				}
				if operation != "missing-commit" && f.files.opens.Load() != 0 {
					t.Fatal("read or invalid preparation reached filesystem")
				}
				untrackedReviewAssertClosed(t, f.files)
			})
		}
	}
}

func TestUntrackedReviewCancellationAndStaleCaptureAtValidation(t *testing.T) {
	for _, change := range []string{"cancel", "capture", "file"} {
		f := untrackedReviewFixture()
		ctx, cancel := context.WithCancel(t.Context())
		f.execution.onValidate = func(n int) {
			if n != 2 {
				return
			}
			switch change {
			case "cancel":
				cancel()
			case "capture":
				f.execution.capture.snapshot.Scope.ClientStamp = "changed"
			case "file":
				f.files.file.Mtime++
			}
		}
		err := f.run(ctx)
		cancel()
		if err == nil || f.execution.completes != 0 || f.metadata.searches != 0 {
			t.Fatalf("validation change reached metadata: %v", err)
		}
		untrackedReviewAssertClosed(t, f.files)
	}
}

func TestUntrackedReviewHistoricalTVInstantsAndRawContent(t *testing.T) {
	f := untrackedReviewFixture()
	m := untrackedReviewMaterialCopy(f.repo.confirmation.Material)
	m.Scope.TargetMediaType, m.Review.Request.Input.MediaType = "tv", "tv"
	c := m.Review.Evidence.Candidate
	c.Namespace, c.Key = "tmdb.tv", "tmdb.tv:42"
	date := time.Date(2020, 1, 15, 0, 0, 0, 0, time.UTC)
	c.Units = []ports.UntrackedReviewUnit{{UnitKey: "S01E03", TMDBEpisodeID: 403, Season: 1, Episode: 3, AirDate: &date}}
	m.Input.CandidateKey = c.Key
	other := untrackedReviewMaterialCopy(m)
	zone := time.FixedZone("original-offset", 8*60*60)
	v := date.In(zone)
	other.Review.Evidence.Candidate.Units[0].AirDate = &v
	other.CreatedAt = other.CreatedAt.In(zone)
	if !untrackedReviewMaterialEqual(m, other) {
		t.Fatal("equivalent instants compared as raw time representations")
	}
	f.repo.confirmation.Material = other
	f.service.files = nil
	before, err := json.Marshal(f.repo.confirmation)
	if err != nil {
		t.Fatal(err)
	}
	out, err := f.service.ConfirmationReceipt(t.Context(), f.actor, f.snapshot.Scope.ID, f.snapshot.File.ID, "confirm-1")
	if err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(out)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("historical bytes were normalized on return")
	}
	other.Review.Evidence.Candidate.Title += " changed"
	if untrackedReviewMaterialEqual(m, other) {
		t.Fatal("full material content was not compared")
	}
	if f.files.opens.Load() != 0 {
		t.Fatal("historical TV receipt performed I/O")
	}
}
