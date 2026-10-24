package search_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/google/uuid"
)

type automaticTVRepo struct {
	ports.SearchRepository
	ports.AutomaticTVRoundRepository
	t                           *testing.T
	work                        ports.AutomaticTVSourceWork
	original                    ports.AutomaticTVSourceWork
	indexer                     ports.SearchIndexer
	specs                       []ports.SearchQuerySpec
	permits                     []*automaticTVPermit
	pages                       map[uuid.UUID]ports.SearchPageEvidence
	rows                        map[uuid.UUID][]ports.SearchCandidate
	outcome                     ports.AutomaticTVSourceOutcome
	finished                    chan struct{}
	denied                      atomic.Bool
	commits                     atomic.Int32
	calls                       atomic.Int32
	loadErr, admitErr, delayErr error
	delay                       time.Duration
	mutateSpec                  func(*ports.SearchQuerySpec)
	sharedState                 string
	beginShared                 bool
}

func (r *automaticTVRepo) LoadAutomaticTVSource(_ context.Context, task ports.AutomaticTVSourceTask) (*ports.AutomaticTVSourceWork, error) {
	if task != r.original.Task {
		r.t.Error("load lost original task")
	}
	if r.loadErr != nil {
		return nil, r.loadErr
	}
	return &r.work, nil
}
func (r *automaticTVRepo) FinishAutomaticTVSource(ctx context.Context, work ports.AutomaticTVSourceWork, outcome ports.AutomaticTVSourceOutcome) error {
	defer close(r.finished)
	if !reflect.DeepEqual(work, r.original) {
		r.t.Error("finish mutated original work")
	}
	if ctx.Err() != nil {
		r.t.Error("terminal context already canceled")
	}
	r.outcome = outcome
	if r.denied.Load() {
		return ports.ErrForbidden
	}
	return nil
}
func (r *automaticTVRepo) GetIndexer(context.Context, uuid.UUID) (ports.SearchIndexer, error) {
	return r.indexer, nil
}
func (r *automaticTVRepo) AdmitQuery(_ context.Context, spec ports.SearchQuerySpec) (ports.SearchQueryAdmission, ports.SearchExecutionPermit, error) {
	if spec.AutomaticTV == nil {
		r.t.Fatal("automatic binding missing")
	}
	q := r.original.Query
	q.Offset = spec.Page * 50
	canonical, _, hash, err := search.CanonicalTVEpisodeQuery(q)
	if err != nil || !reflect.DeepEqual(spec.AutomaticTV.Query, canonical) || spec.AutomaticTV.Task != r.original.Task || spec.AutomaticTV.SnapshotHash != r.original.SnapshotHash || spec.AutomaticTV.ParametersHash != hash || spec.Keyword != "tv-episode-query-v1:"+hash || spec.ConfigVersion != r.original.ConfigVersion || spec.IndexerID != r.original.Task.IndexerID || spec.ID == uuid.Nil {
		r.t.Fatal("incorrect frozen automatic spec")
	}
	if spec.RSSJobID != uuid.Nil || spec.ProbeOnly || spec.Interactive != nil || spec.Actor != nil || spec.HealthJob != nil {
		r.t.Fatal("automatic query mixed with another authority")
	}
	if r.denied.Load() {
		return ports.SearchQueryAdmission{}, nil, ports.ErrForbidden
	}
	if r.admitErr != nil {
		return ports.SearchQueryAdmission{}, nil, r.admitErr
	}
	if r.sharedState != "" && !r.beginShared {
		return ports.SearchQueryAdmission{Shared: &ports.IndexerQueryResult{QueryID: spec.ID, State: r.sharedState, FromShared: true}}, nil, nil
	}
	frozen := spec
	binding := *spec.AutomaticTV
	binding.Query.Categories = slices.Clone(binding.Query.Categories)
	frozen.AutomaticTV = &binding
	r.specs = append(r.specs, frozen)
	p := &automaticTVPermit{repo: r, spec: frozen, closed: make(chan struct{})}
	r.permits = append(r.permits, p)
	if r.mutateSpec != nil {
		r.mutateSpec(&spec)
	}
	return ports.SearchQueryAdmission{Indexer: r.indexer}, p, nil
}
func (r *automaticTVRepo) ReadPage(_ context.Context, id uuid.UUID) (ports.SearchPageEvidence, error) {
	p, ok := r.pages[id]
	if !ok {
		return p, ports.ErrBadResponse
	}
	return p, nil
}
func (r *automaticTVRepo) ReadQueryCandidates(_ context.Context, id uuid.UUID) ([]ports.SearchCandidate, error) {
	return r.rows[id], nil
}
func (r *automaticTVRepo) ContinuationDelay(_ context.Context, id uuid.UUID, version int64) (time.Duration, error) {
	if id != r.original.Task.IndexerID || version != r.original.ConfigVersion {
		r.t.Error("continuation lost frozen source version")
	}
	return r.delay, r.delayErr
}

type automaticTVPermit struct {
	repo     *automaticTVRepo
	spec     ports.SearchQuerySpec
	closed   chan struct{}
	failure  ports.SearchQueryFailure
	retry    time.Duration
	rejected bool
}

func (p *automaticTVPermit) BeginQuery(context.Context) (*ports.IndexerQueryResult, error) {
	if p.repo.denied.Load() {
		return nil, ports.ErrForbidden
	}
	if p.repo.beginShared {
		return &ports.IndexerQueryResult{QueryID: p.spec.ID, State: p.repo.sharedState, FromShared: true}, nil
	}
	return &ports.IndexerQueryResult{QueryID: p.spec.ID, State: "running"}, nil
}
func (*automaticTVPermit) ReserveHealthSearch(context.Context) error {
	panic("automatic query requested health budget")
}
func (p *automaticTVPermit) CommitQuery(ctx context.Context, c ports.SearchQueryCommit) (*ports.IndexerQueryResult, error) {
	if p.repo.denied.Load() || ctx.Err() != nil {
		p.rejected = true
		return nil, ports.ErrForbidden
	}
	if c.Page == nil || c.Page.QueryID != p.spec.ID || c.Page.PageOffset != int64(p.spec.Page)*50 || c.Page.PageLimit != 50 {
		p.repo.t.Fatal("bad committed evidence")
	}
	for _, candidate := range c.Candidates {
		if candidate.SourceConfigVersion != p.spec.ConfigVersion || candidate.IndexerID != p.spec.IndexerID || len(candidate.LinkEncrypted) == 0 {
			p.repo.t.Error("candidate did not reuse preparation")
		}
	}
	p.repo.pages[p.spec.ID] = *c.Page
	p.repo.rows[p.spec.ID] = c.Candidates
	p.repo.commits.Add(1)
	return &ports.IndexerQueryResult{QueryID: p.spec.ID, State: "succeeded", ResultsCount: len(c.Candidates)}, nil
}
func (p *automaticTVPermit) FailQuery(ctx context.Context, f ports.SearchQueryFailure, retry time.Duration) error {
	if p.repo.denied.Load() {
		p.rejected = true
		return ports.ErrForbidden
	}
	if ctx.Err() != nil {
		p.repo.t.Error("failure used canceled context")
	}
	p.failure = f
	p.retry = retry
	return nil
}
func (p *automaticTVPermit) Close() { close(p.closed) }

type automaticTVClient struct {
	repo *automaticTVRepo
	call func(context.Context, ports.TVEpisodeQuery) (*ports.IndexerPage, error)
}

func (*automaticTVClient) Search(context.Context, string, []int32, int32, int32) ([]*ports.ReleaseItem, error) {
	panic("automatic TV fell back to movie search")
}
func (*automaticTVClient) FetchTorrent(context.Context, string) ([]byte, error) {
	panic("source attempted acquisition")
}
func (*automaticTVClient) Capabilities(context.Context) (ports.IndexerCapabilities, error) {
	panic("automatic query requested extra capabilities I/O")
}
func (c *automaticTVClient) SearchTVEpisodePage(ctx context.Context, q ports.TVEpisodeQuery) (*ports.IndexerPage, error) {
	c.repo.calls.Add(1)
	if q.Keyword != "Example Show 2024" || q.Mode != "tvsearch" || q.SeriesNamespace != "tmdb.tv" || q.SeriesID != 1234 || q.Season != 1 || q.Episode != 2 || q.Limit != 50 || !slices.Equal(q.Categories, []int32{5030, 5040}) {
		c.repo.t.Fatal("network did not receive complete canonical real-keyword query")
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 2*time.Second {
		c.repo.t.Fatal("missing per-page deadline")
	}
	return c.call(ctx, q)
}

func newAutomaticTVHarness(t *testing.T) (*search.Service, *automaticTVRepo, *automaticTVClient) {
	t.Helper()
	task := ports.AutomaticTVSourceTask{RoundID: uuid.New(), IndexerID: uuid.New(), Lease: ports.JobLease{ID: uuid.New(), Owner: "original-worker", Token: 7}}
	work := ports.AutomaticTVSourceWork{Task: task, SnapshotHash: strings.Repeat("a", 64), ConfigVersion: 5, Deadline: time.Now().Add(30 * time.Second), Query: ports.TVEpisodeQuery{SchemaVersion: search.TVEpisodeQuerySchemaVersion, Mode: "tvsearch", SeriesNamespace: "tmdb.tv", SeriesID: 1234, Keyword: "Example Show 2024", Season: 1, Episode: 2, Categories: []int32{5040, 5030}, Limit: 50}}
	original := work
	original.Query.Categories = slices.Clone(work.Query.Categories)
	r := &automaticTVRepo{t: t, work: work, original: original, indexer: ports.SearchIndexer{ID: task.IndexerID, ConfigVersion: 5, Categories: []byte("[5030,5040]"), Health: "online"}, finished: make(chan struct{}), pages: make(map[uuid.UUID]ports.SearchPageEvidence), rows: make(map[uuid.UUID][]ports.SearchCandidate)}
	codec := crypto.NewSecretProtector(make([]byte, 32))
	key, err := codec.EncryptSecret([]byte("provider-key"))
	if err != nil {
		t.Fatal(err)
	}
	r.indexer.ApiKeyEncrypted = key
	svc := search.NewService(r, executionBoundaryIDs{}, codec, 0)
	svc.SetAutomaticTVRoundRepository(r)
	c := &automaticTVClient{repo: r, call: func(_ context.Context, q ports.TVEpisodeQuery) (*ports.IndexerPage, error) {
		return automaticTVPage(q, 0, 0), nil
	}}
	svc.SetConfiguredClientFactory(func(protocol ports.IndexerProtocol, url, key string) ports.IndexerClient {
		if key != "provider-key" {
			t.Error("credential decryption not reused")
		}
		return c
	})
	// A stored protocol routes through the configured factory and decrypted key.
	r.indexer.Protocol = ports.IndexerProtocol("torznab")
	return svc, r, c
}
func automaticTVPage(q ports.TVEpisodeQuery, count int, total int64) *ports.IndexerPage {
	items := make([]*ports.ReleaseItem, count)
	for i := range items {
		guid := fmt.Sprintf("episode-%d", int(q.Offset)+i)
		items[i] = &ports.ReleaseItem{Title: "Example.Show.2024.S01E02.1080p.WEB-DL", GUID: guid, DownloadURL: "https://source.invalid/" + guid, SizeBytes: 1024}
	}
	return &ports.IndexerPage{Offset: q.Offset, Limit: 50, OffsetVerified: true, Total: &total, Items: items}
}

func TestAutomaticTVSourceBoundedPagesAndProof(t *testing.T) {
	for _, tc := range []struct {
		name    string
		total   int64
		pages   int
		outcome ports.AutomaticTVSourceOutcome
	}{
		{"empty", 0, 1, ports.AutomaticTVSourceSucceeded},
		{"short_proven", 3, 1, ports.AutomaticTVSourceSucceeded},
		{"two_full_pages", 100, 2, ports.AutomaticTVSourceSucceeded},
		{"four_full_pages", 250, 4, ports.AutomaticTVSourcePartial},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, r, c := newAutomaticTVHarness(t)
			c.call = func(_ context.Context, q ports.TVEpisodeQuery) (*ports.IndexerPage, error) {
				return automaticTVPage(q, int(min(int64(50), tc.total-int64(q.Offset))), tc.total), nil
			}
			if err := svc.ExecuteAutomaticTVSource(context.Background(), r.work.Task); err != nil {
				t.Fatal(err)
			}
			if r.outcome != tc.outcome || int(r.calls.Load()) != tc.pages || int(r.commits.Load()) != tc.pages {
				t.Fatal(r.outcome, r.calls.Load(), r.commits.Load())
			}
			hashes := make(map[string]bool)
			for _, spec := range r.specs {
				if hashes[spec.AutomaticTV.ParametersHash] {
					t.Error("page did not change parameter hash")
				}
				hashes[spec.AutomaticTV.ParametersHash] = true
				select {
				case <-r.permits[spec.Page].closed:
				default:
					t.Error("permit not closed")
				}
			}
		})
	}
}

func TestAutomaticTVSourceConservativeEvidence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		change  func(*ports.IndexerPage)
		commits int32
		outcome ports.AutomaticTVSourceOutcome
	}{
		{"unproven_short", func(p *ports.IndexerPage) { p.OffsetVerified = false; p.Total = nil }, 1, ports.AutomaticTVSourcePartial},
		{"missing_total", func(p *ports.IndexerPage) { p.Total = nil }, 1, ports.AutomaticTVSourcePartial},
		{"wrong_offset", func(p *ports.IndexerPage) { p.Offset = 50 }, 0, ports.AutomaticTVSourceFailed},
		{"negative_total", func(p *ports.IndexerPage) { v := int64(-1); p.Total = &v }, 0, ports.AutomaticTVSourceFailed},
		{"short_before_total", func(p *ports.IndexerPage) { v := int64(100); p.Total = &v }, 1, ports.AutomaticTVSourcePartial},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, r, c := newAutomaticTVHarness(t)
			c.call = func(_ context.Context, q ports.TVEpisodeQuery) (*ports.IndexerPage, error) {
				p := automaticTVPage(q, 3, 3)
				tc.change(p)
				return p, nil
			}
			if err := svc.ExecuteAutomaticTVSource(context.Background(), r.work.Task); !errors.Is(err, ports.ErrBadResponse) {
				t.Fatal(err)
			}
			if r.outcome != tc.outcome || r.calls.Load() != 1 || r.commits.Load() != tc.commits {
				t.Fatal(r.outcome, r.calls.Load(), r.commits.Load())
			}
		})
	}
}

func TestAutomaticTVSourcePreservesFirstPage(t *testing.T) {
	for _, kind := range []string{"changed_total", "duplicate", "timeout", "deferred", "wait_too_long", "wait_error"} {
		t.Run(kind, func(t *testing.T) {
			svc, r, c := newAutomaticTVHarness(t)
			c.call = func(_ context.Context, q ports.TVEpisodeQuery) (*ports.IndexerPage, error) {
				if q.Offset == 0 {
					switch kind {
					case "deferred":
						r.delayErr = ports.ErrAutomaticTVSearchDeferred
					case "wait_too_long":
						r.delay = 9 * time.Second
					case "wait_error":
						r.delayErr = errors.New("private provider URL/key")
					}
					return automaticTVPage(q, 50, 250), nil
				}
				switch kind {
				case "timeout":
					return nil, ports.ErrTimeout
				case "changed_total":
					return automaticTVPage(q, 50, 300), nil
				case "duplicate":
					p := automaticTVPage(q, 50, 250)
					p.Items[0].GUID = "episode-0"
					return p, nil
				}
				t.Fatal("continuation should not call network")
				return nil, ports.ErrUnavailable
			}
			err := svc.ExecuteAutomaticTVSource(context.Background(), r.work.Task)
			if err == nil || strings.Contains(err.Error(), "private") || r.outcome != ports.AutomaticTVSourcePartial || r.commits.Load() != 1 {
				t.Fatal(err, r.outcome, r.commits.Load())
			}
		})
	}
}

func TestAutomaticTVSourceFailureCategories(t *testing.T) {
	for _, tc := range []struct {
		name           string
		err, errorWant error
		outcome        ports.AutomaticTVSourceOutcome
		failure        ports.SearchQueryFailure
		retry          time.Duration
	}{
		{"timeout", ports.ErrTimeout, ports.ErrTimeout, ports.AutomaticTVSourceTimeout, ports.SearchFailureTimeout, 0},
		{"auth", ports.ErrAuthRequired, ports.ErrAuthRequired, ports.AutomaticTVSourceFailed, ports.SearchFailureAuth, 0},
		{"bad_response", ports.ErrBadResponse, ports.ErrBadResponse, ports.AutomaticTVSourceFailed, ports.SearchFailureBadResponse, 0},
		{"unsupported", ports.ErrRejected, ports.ErrRejected, ports.AutomaticTVSourceFailed, ports.SearchFailureUnsupported, 0},
		{"private_error", errors.New("https://source.invalid/?passkey=private-key"), ports.ErrUnavailable, ports.AutomaticTVSourceFailed, ports.SearchFailureUnavailable, 0},
		{"rate_limited", ports.NewRateLimitError(17*time.Second, "private-key"), ports.ErrUnavailable, ports.AutomaticTVSourceFailed, ports.SearchFailureRateLimited, 17 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, r, c := newAutomaticTVHarness(t)
			c.call = func(context.Context, ports.TVEpisodeQuery) (*ports.IndexerPage, error) { return nil, tc.err }
			err := svc.ExecuteAutomaticTVSource(context.Background(), r.work.Task)
			if !errors.Is(err, tc.errorWant) || r.outcome != tc.outcome || r.commits.Load() != 0 || r.permits[0].failure != tc.failure || r.permits[0].retry != tc.retry {
				t.Fatal(err, r.outcome, r.permits[0].failure, r.permits[0].retry)
			}
		})
	}
	t.Run("admission_deferred", func(t *testing.T) {
		svc, r, _ := newAutomaticTVHarness(t)
		r.admitErr = ports.ErrAutomaticTVSearchDeferred
		if err := svc.ExecuteAutomaticTVSource(context.Background(), r.work.Task); !errors.Is(err, ports.ErrAutomaticTVSearchDeferred) || r.outcome != ports.AutomaticTVSourceDeferred || r.calls.Load() != 0 {
			t.Fatal(err, r.outcome)
		}
	})
}

func TestAutomaticTVSourceInvalidWorkHasZeroIO(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*ports.AutomaticTVSourceWork)
	}{
		{"version", func(w *ports.AutomaticTVSourceWork) { w.ConfigVersion = 0 }},
		{"hash", func(w *ports.AutomaticTVSourceWork) { w.SnapshotHash = "untrusted" }},
		{"season", func(w *ports.AutomaticTVSourceWork) { w.Query.Season = 0 }},
		{"mode", func(w *ports.AutomaticTVSourceWork) { w.Query.Mode = "search" }},
		{"nil_categories", func(w *ports.AutomaticTVSourceWork) { w.Query.Categories = nil }},
		{"duplicate_categories", func(w *ports.AutomaticTVSourceWork) { w.Query.Categories = []int32{5030, 5030} }},
		{"saved_mismatch", func(w *ports.AutomaticTVSourceWork) { w.NextPage = 1 }},
		{"page_limit", func(w *ports.AutomaticTVSourceWork) { w.NextPage = 5 }},
		{"offset", func(w *ports.AutomaticTVSourceWork) { w.Query.Offset = 50 }},
		{"deadline", func(w *ports.AutomaticTVSourceWork) { w.Deadline = time.Time{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, r, _ := newAutomaticTVHarness(t)
			tc.change(&r.work)
			r.original = r.work
			if err := svc.ExecuteAutomaticTVSource(context.Background(), r.work.Task); !errors.Is(err, ports.ErrBadResponse) || r.calls.Load() != 0 || r.commits.Load() != 0 {
				t.Fatal(err, r.calls.Load())
			}
		})
	}
}

func TestAutomaticTVSourceCategoriesAndCloning(t *testing.T) {
	t.Run("configured_mismatch", func(t *testing.T) {
		svc, r, _ := newAutomaticTVHarness(t)
		r.indexer.Categories = []byte("[5000]")
		if err := svc.ExecuteAutomaticTVSource(context.Background(), r.work.Task); !errors.Is(err, ports.ErrBadResponse) || r.calls.Load() != 0 || r.permits[0].failure != ports.SearchFailureBadResponse {
			t.Fatal(err)
		}
	})
	t.Run("independent_snapshots", func(t *testing.T) {
		svc, r, c := newAutomaticTVHarness(t)
		r.mutateSpec = func(spec *ports.SearchQuerySpec) {
			spec.AutomaticTV.Query.Categories[0] = 9999
			spec.AutomaticTV.Task.Lease.Owner = "forged"
		}
		c.call = func(_ context.Context, q ports.TVEpisodeQuery) (*ports.IndexerPage, error) {
			r.work.Query.Categories[0] = 9998
			r.work.Task.Lease.Owner = "rewritten-after-load"
			q.Categories[0] = 9997
			return automaticTVPage(q, 0, 0), nil
		}
		if err := svc.ExecuteAutomaticTVSource(context.Background(), r.original.Task); err != nil {
			t.Fatal(err)
		}
		if r.outcome != ports.AutomaticTVSourceSucceeded {
			t.Fatal(r.outcome)
		}
	})
}

type automaticTVUntypedClient struct{ called bool }

func (c *automaticTVUntypedClient) Search(context.Context, string, []int32, int32, int32) ([]*ports.ReleaseItem, error) {
	c.called = true
	return nil, nil
}
func (*automaticTVUntypedClient) FetchTorrent(context.Context, string) ([]byte, error) {
	panic("unexpected fetch")
}
func TestAutomaticTVSourceNoMovieFallback(t *testing.T) {
	svc, r, _ := newAutomaticTVHarness(t)
	c := &automaticTVUntypedClient{}
	svc.RegisterClient(r.work.Task.IndexerID, c)
	if err := svc.ExecuteAutomaticTVSource(context.Background(), r.work.Task); !errors.Is(err, ports.ErrRejected) || c.called || r.permits[0].failure != ports.SearchFailureUnsupported {
		t.Fatal(err, c.called)
	}
}

func TestAutomaticTVSourceWaitsForPhysicalReturn(t *testing.T) {
	for _, kind := range []string{"cancel", "network_deadline", "lost_owner"} {
		t.Run(kind, func(t *testing.T) {
			svc, r, c := newAutomaticTVHarness(t)
			entered := make(chan context.Context, 1)
			canceled := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			c.call = func(ctx context.Context, q ports.TVEpisodeQuery) (*ports.IndexerPage, error) {
				entered <- ctx
				if kind != "lost_owner" {
					<-ctx.Done()
					close(canceled)
				}
				<-release
				return automaticTVPage(q, 1, 1), nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- svc.ExecuteAutomaticTVSource(ctx, r.original.Task) }()
			var networkCtx context.Context
			select {
			case networkCtx = <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("client not entered")
			}
			switch kind {
			case "cancel":
				cancel()
				<-canceled
			case "network_deadline":
				select {
				case <-canceled:
				case <-time.After(3 * time.Second):
					t.Fatal("2s network deadline not observed")
				}
				if !errors.Is(networkCtx.Err(), context.DeadlineExceeded) {
					t.Fatal(networkCtx.Err())
				}
			case "lost_owner":
				r.denied.Store(true)
			}
			select {
			case <-r.permits[0].closed:
				t.Error("physical permit closed while I/O active")
			default:
			}
			select {
			case <-r.finished:
				t.Error("source finished while I/O active")
			default:
			}
			select {
			case err := <-done:
				t.Fatalf("execution detached active I/O: %v", err)
			default:
			}
			unblock()
			var err error
			select {
			case err = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("did not join physical call")
			}
			if err == nil || r.commits.Load() != 0 {
				t.Fatal(err, r.commits.Load())
			}
			select {
			case <-r.permits[0].closed:
			default:
				t.Fatal("permit not released after return")
			}
			if kind == "lost_owner" {
				if !errors.Is(err, ports.ErrForbidden) || !r.permits[0].rejected {
					t.Fatal(err, "late commit not rejected")
				}
			} else {
				if r.outcome != ports.AutomaticTVSourceTimeout {
					t.Fatal(r.outcome)
				}
				if kind == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				if kind == "network_deadline" && !errors.Is(err, ports.ErrTimeout) {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestAutomaticTVSourceCurrentAuthorityAndConfig(t *testing.T) {
	t.Run("role_before_io", func(t *testing.T) {
		svc, r, _ := newAutomaticTVHarness(t)
		r.denied.Store(true)
		if err := svc.ExecuteAutomaticTVSource(context.Background(), r.work.Task); !errors.Is(err, ports.ErrForbidden) || r.calls.Load() != 0 {
			t.Fatal(err)
		}
	})
	t.Run("config_changed", func(t *testing.T) {
		svc, r, _ := newAutomaticTVHarness(t)
		r.indexer.ConfigVersion++
		if err := svc.ExecuteAutomaticTVSource(context.Background(), r.work.Task); !errors.Is(err, ports.ErrUnavailable) || r.calls.Load() != 0 {
			t.Fatal(err)
		}
	})
	t.Run("expired_round", func(t *testing.T) {
		svc, r, _ := newAutomaticTVHarness(t)
		r.work.Deadline = time.Now().Add(-time.Second)
		r.original = r.work
		if err := svc.ExecuteAutomaticTVSource(context.Background(), r.work.Task); !errors.Is(err, ports.ErrTimeout) || r.calls.Load() != 0 || r.outcome != ports.AutomaticTVSourceTimeout {
			t.Fatal(err, r.outcome)
		}
	})
}

func TestAutomaticTVSourceActiveSharedQueryNeverResends(t *testing.T) {
	for _, begin := range []bool{false, true} {
		t.Run(fmt.Sprint(begin), func(t *testing.T) {
			svc, r, _ := newAutomaticTVHarness(t)
			r.sharedState = "running"
			r.beginShared = begin
			if err := svc.ExecuteAutomaticTVSource(context.Background(), r.work.Task); !errors.Is(err, ports.ErrAutomaticTVSearchDeferred) || r.calls.Load() != 0 || r.outcome != ports.AutomaticTVSourceDeferred {
				t.Fatal(err, r.outcome)
			}
		})
	}
}

func TestAutomaticTVSourceRejectsForeignLoadedTask(t *testing.T) {
	svc, r, _ := newAutomaticTVHarness(t)
	task := r.original.Task
	r.work.Task.Lease.Token++
	if err := svc.ExecuteAutomaticTVSource(context.Background(), task); !errors.Is(err, ports.ErrBadResponse) || r.calls.Load() != 0 {
		t.Fatal(err)
	}
	select {
	case <-r.finished:
		t.Fatal("finished a foreign loaded task")
	default:
	}
}

func TestAutomaticTVSourceLostAuthorityRejectsFailureReceipt(t *testing.T) {
	svc, r, c := newAutomaticTVHarness(t)
	c.call = func(context.Context, ports.TVEpisodeQuery) (*ports.IndexerPage, error) {
		r.denied.Store(true)
		return nil, ports.ErrTimeout
	}
	if err := svc.ExecuteAutomaticTVSource(context.Background(), r.original.Task); !errors.Is(err, ports.ErrForbidden) || !r.permits[0].rejected || r.commits.Load() != 0 {
		t.Fatal(err)
	}
}

type automaticTVLegacyRepo struct{ *executionBoundaryRepo }

func (*automaticTVLegacyRepo) ReadPage(context.Context, uuid.UUID) (ports.SearchPageEvidence, error) {
	return ports.SearchPageEvidence{}, nil
}

func TestAutomaticTVSourcePreservesLegacyRoutes(t *testing.T) {
	for _, route := range []string{"movie", "rss", "interactive"} {
		t.Run(route, func(t *testing.T) {
			p := &executionBoundaryPermit{}
			idx := ports.SearchIndexer{ID: uuid.New(), ConfigVersion: 3, Categories: []byte("[]")}
			repo := &automaticTVLegacyRepo{&executionBoundaryRepo{indexer: idx, permit: p}}
			codec := crypto.NewSecretProtector(make([]byte, 32))
			svc := search.NewService(repo, executionBoundaryIDs{}, codec)
			svc.RegisterClient(idx.ID, &executionBoundaryClient{p: p, items: []*ports.ReleaseItem{{Title: "Example.Movie.2024.1080p.WEB-DL", GUID: "legacy-guid", DownloadURL: "https://source.invalid/legacy", SizeBytes: 1024}}})
			rssJob := uuid.New()
			var err error
			switch route {
			case "movie":
				_, err = svc.ExecuteSearch(context.Background(), idx.ID, "Original Movie", 2)
			case "rss":
				_, err = svc.ExecuteRSS(context.Background(), idx.ID, rssJob, 3)
			case "interactive":
				err = svc.ExecuteInteractiveSearchSource(context.Background(), ports.InteractiveSearchSourceLease{Task: ports.InteractiveSearchSourceTask{SearchID: uuid.New(), IndexerID: idx.ID, ConfigVersion: 3, Lease: ports.JobLease{ID: uuid.New(), Owner: "legacy-owner", Token: 1}}, ActorID: uuid.New(), Query: "Original Movie", Deadline: time.Now().Add(5 * time.Second)})
			}
			if err != nil || repo.spec.AutomaticTV != nil || p.commit.Page == nil || p.commit.Page.ItemCount != 1 || len(p.commit.Candidates) != 1 || !reflect.DeepEqual(p.events, []string{"admit", "begin", "network", "commit", "close"}) {
				t.Fatal(err, repo.spec, p.events)
			}
			if repo.spec.MinimumInterval != 2*time.Second {
				t.Fatal("old budget changed")
			}
			switch route {
			case "movie":
				if repo.spec.Keyword != "Original Movie" || repo.spec.Page != 2 || p.commit.Page.PageOffset != 100 {
					t.Fatal(repo.spec)
				}
			case "rss":
				if repo.spec.RSSJobID != rssJob || repo.spec.Keyword != "" {
					t.Fatal(repo.spec)
				}
			case "interactive":
				if repo.spec.Interactive == nil || repo.spec.Interactive.Query != "Original Movie" {
					t.Fatal(repo.spec)
				}
			}
		})
	}
}

type automaticTVParsingSecrets struct {
	ports.SearchSecrets
	beforeEncrypt func()
}

func (s automaticTVParsingSecrets) EncryptSecret(plain []byte) ([]byte, error) {
	s.beforeEncrypt()
	return s.SearchSecrets.EncryptSecret(plain)
}

func TestAutomaticTVSourceNetworkBudgetExcludesParsing(t *testing.T) {
	for _, cancelParsing := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelParsing), func(t *testing.T) {
			_, r, c := newAutomaticTVHarness(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var networkCtx context.Context
			c.call = func(call context.Context, q ports.TVEpisodeQuery) (*ports.IndexerPage, error) {
				networkCtx = call
				return automaticTVPage(q, 1, 1), nil
			}
			codec := automaticTVParsingSecrets{SearchSecrets: crypto.NewSecretProtector(make([]byte, 32)), beforeEncrypt: func() {
				if networkCtx == nil || networkCtx.Err() == nil || ctx.Err() != nil {
					t.Error("network context was not ended separately from parsing")
				}
				if cancelParsing {
					cancel()
				}
			}}
			svc := search.NewService(r, executionBoundaryIDs{}, codec)
			svc.SetAutomaticTVRoundRepository(r)
			svc.RegisterClient(r.original.Task.IndexerID, c)
			err := svc.ExecuteAutomaticTVSource(ctx, r.original.Task)
			if cancelParsing {
				if !errors.Is(err, context.Canceled) || r.commits.Load() != 0 {
					t.Fatal(err, r.commits.Load())
				}
			} else if err != nil || r.commits.Load() != 1 || r.outcome != ports.AutomaticTVSourceSucceeded {
				t.Fatal(err, r.outcome)
			}
		})
	}
}

func TestAutomaticTVSourceSavedPagesNeverResend(t *testing.T) {
	svc, r, c := newAutomaticTVHarness(t)
	id := uuid.New()
	total := int64(51)
	r.work.NextPage = 1
	r.work.SavedQueries = []uuid.UUID{id}
	r.original = r.work
	r.original.SavedQueries = slices.Clone(r.work.SavedQueries)
	r.pages[id] = ports.SearchPageEvidence{QueryID: id, PageOffset: 0, PageLimit: 50, ItemCount: 50, OffsetVerified: true, Total: &total}
	for i := range 50 {
		r.rows[id] = append(r.rows[id], ports.SearchCandidate{Guid: fmt.Sprintf("saved-%d", i)})
	}
	c.call = func(_ context.Context, q ports.TVEpisodeQuery) (*ports.IndexerPage, error) {
		if q.Offset != 50 {
			t.Fatal("resent saved page")
		}
		r.work.SavedQueries[0] = uuid.New()
		return automaticTVPage(q, 1, total), nil
	}
	if err := svc.ExecuteAutomaticTVSource(context.Background(), r.work.Task); err != nil || r.calls.Load() != 1 || r.outcome != ports.AutomaticTVSourceSucceeded {
		t.Fatal(err, r.outcome)
	}
}
