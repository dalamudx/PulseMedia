package metadatafallback_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/metadatafallback"
)

type fixture struct {
	ports.TVDBProvider
	identity      ports.TVArtworkIdentity
	season        *ports.TVDBSeasonMetadata
	matches       map[int64]*ports.TVDBEpisodeResolution
	failure       error
	identityCalls atomic.Int32
	resolveCalls  atomic.Int32
	onResolve     func(context.Context, int64) (*ports.TVDBEpisodeResolution, error)
}

func newFixture() *fixture {
	return &fixture{identity: ports.TVArtworkIdentity{TMDBID: 1396, TVDBID: 81189}, season: &ports.TVDBSeasonMetadata{TVDBID: 81189, SeasonNumber: 1, Numbering: "official", Complete: true, Episodes: []ports.TVDBEpisode{{TVDBID: 9001, SeasonNumber: 1, EpisodeNumber: 1}, {TVDBID: 9002, SeasonNumber: 1, EpisodeNumber: 3}}}, matches: map[int64]*ports.TVDBEpisodeResolution{
		9001: {TMDBSeriesID: 1396, TVDBEpisodeID: 9001, SeasonNumber: 1, Episode: ports.TVEpisodeMetadata{TMDBEpisodeID: 200, EpisodeNumber: 2}},
		9002: {TMDBSeriesID: 1396, TVDBEpisodeID: 9002, SeasonNumber: 1, Episode: ports.TVEpisodeMetadata{TMDBEpisodeID: 700, EpisodeNumber: 7}},
	}}
}
func (f *fixture) ResolveTVArtworkIdentity(context.Context, int64) (ports.TVArtworkIdentity, error) {
	f.identityCalls.Add(1)
	return f.identity, nil
}
func (f *fixture) GetSeasonMetadata(context.Context, int64, int) (*ports.TVDBSeasonMetadata, error) {
	return f.season, f.failure
}
func (f *fixture) ResolveTVDBEpisode(ctx context.Context, series, id int64) (*ports.TVDBEpisodeResolution, error) {
	f.resolveCalls.Add(1)
	if series != 1396 {
		return nil, ports.ErrBadResponse
	}
	if f.onResolve != nil {
		return f.onResolve(ctx, id)
	}
	found, ok := f.matches[id]
	if !ok {
		return nil, ports.ErrNotFound
	}
	return found, nil
}
func primary() *ports.TVSeasonMetadata {
	return &ports.TVSeasonMetadata{SeasonNumber: 1, Complete: true, Episodes: []ports.TVEpisodeMetadata{{TMDBEpisodeID: 200, EpisodeNumber: 2}}}
}
func TestSupplementPreservesSparseCanonicalIdentity(t *testing.T) {
	f := newFixture()
	base := primary()
	original := time.Date(2008, 1, 20, 0, 0, 0, 0, time.UTC)
	foreignDate := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	base.Episodes[0].AirDate = &original
	f.matches[9001].Episode.AirDate = &foreignDate
	f.season.Episodes[1].AirDate = &foreignDate
	f.season.Episodes[1].AbsoluteNumber = new(99)
	f.season.Episodes = append(f.season.Episodes, ports.TVDBEpisode{TVDBID: 9003, SeasonNumber: 1, EpisodeNumber: 4}, ports.TVDBEpisode{TVDBID: 9004, SeasonNumber: 1, EpisodeNumber: 5})
	f.matches[9004] = &ports.TVDBEpisodeResolution{TMDBSeriesID: 1396, TVDBEpisodeID: 9004, SeasonNumber: 2, Episode: ports.TVEpisodeMetadata{TMDBEpisodeID: 1000, EpisodeNumber: 1}}
	result, err := metadatafallback.New(f, f, f).ReadSeasonSupplement(t.Context(), 1396, 1, base)
	if err != nil || result.Metadata.Complete || result.TMDBSeriesID != 1396 || result.TVDBSeriesID != 81189 || len(result.Metadata.Episodes) != 2 || len(result.Added) != 1 || len(result.UnmatchedTVDBIDs) != 2 {
		t.Fatalf("invalid supplemental observation: %+v %v", result, err)
	}
	first, added := result.Metadata.Episodes[0], result.Metadata.Episodes[1]
	if first.TMDBEpisodeID != 200 || first.EpisodeNumber != 2 || !first.AirDate.Equal(original) || added.TMDBEpisodeID != 700 || added.EpisodeNumber != 7 || added.AirDate != nil || added.AbsoluteNumber != nil || result.Added[0].TVDBEpisodeID != 9002 {
		t.Fatal("source coordinates or dates replaced canonical facts")
	}
	*first.AirDate = foreignDate
	if !base.Episodes[0].AirDate.Equal(original) || len(base.Episodes) != 1 {
		t.Fatal("caller snapshot aliased")
	}
}
func TestSupplementCanonicalCollisionsRejectAll(t *testing.T) {
	cases := map[string]func(*fixture){
		"occupied_coordinate":           func(f *fixture) { f.matches[9002].Episode.EpisodeNumber = 2 },
		"existing_identity_moved":       func(f *fixture) { f.matches[9002].Episode.TMDBEpisodeID = 200 },
		"two_source_ids_same_canonical": func(f *fixture) { f.matches[9002].Episode = f.matches[9001].Episode },
		"foreign_series":                func(f *fixture) { f.matches[9002].TMDBSeriesID = 1397 },
		"foreign_source_id":             func(f *fixture) { f.matches[9002].TVDBEpisodeID = 9009 },
		"missing_canonical_identity":    func(f *fixture) { f.matches[9002].Episode.TMDBEpisodeID = 0 },
		"unsupported_absolute":          func(f *fixture) { f.matches[9002].Episode.AbsoluteNumber = new(7) },
		"nil_match":                     func(f *fixture) { f.matches[9002] = nil },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture()
			change(f)
			result, err := metadatafallback.New(f, f, f).ReadSeasonSupplement(t.Context(), 1396, 1, primary())
			if result != nil || !errors.Is(err, ports.ErrBadResponse) {
				t.Fatalf("partial collision accepted: %+v %v", result, err)
			}
		})
	}
}
func TestSupplementRejectsUnprovenSourceBeforeResolution(t *testing.T) {
	cases := map[string]func(*fixture){
		"identity_mismatch":     func(f *fixture) { f.identity.TMDBID = 1397 },
		"missing_tvdb_identity": func(f *fixture) { f.identity.TVDBID = 0 },
		"foreign_series":        func(f *fixture) { f.season.TVDBID = 81190 },
		"foreign_season":        func(f *fixture) { f.season.SeasonNumber = 2 },
		"dvd_numbering":         func(f *fixture) { f.season.Numbering = "dvd" },
		"incomplete":            func(f *fixture) { f.season.Complete = false },
		"duplicate_id":          func(f *fixture) { f.season.Episodes[1].TVDBID = 9001 },
		"duplicate_coordinate":  func(f *fixture) { f.season.Episodes[1].EpisodeNumber = 1 },
		"wrong_episode_parent":  func(f *fixture) { f.season.Episodes[1].SeasonNumber = 2 },
		"missing_episode_id":    func(f *fixture) { f.season.Episodes[1].TVDBID = 0 },
		"nil_snapshot":          func(f *fixture) { f.season = nil },
		"bounded_resolution":    func(f *fixture) { f.season.Episodes = make([]ports.TVDBEpisode, 65) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture()
			change(f)
			result, err := metadatafallback.New(f, f, f).ReadSeasonSupplement(t.Context(), 1396, 1, primary())
			if result != nil || !errors.Is(err, ports.ErrBadResponse) || f.resolveCalls.Load() != 0 {
				t.Fatalf("unproven source triggered lookups: %v", err)
			}
		})
	}
}
func TestSupplementPrimaryValidationPrecedesNetwork(t *testing.T) {
	cases := map[string]func(*ports.TVSeasonMetadata){
		"incomplete":     func(p *ports.TVSeasonMetadata) { p.Complete = false },
		"wrong_season":   func(p *ports.TVSeasonMetadata) { p.SeasonNumber = 2 },
		"unknown_id":     func(p *ports.TVSeasonMetadata) { p.Episodes[0].TMDBEpisodeID = 0 },
		"invalid_number": func(p *ports.TVSeasonMetadata) { p.Episodes[0].EpisodeNumber = 0 },
		"duplicate_id":   func(p *ports.TVSeasonMetadata) { p.Episodes = append(p.Episodes, p.Episodes[0]) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture()
			p := primary()
			change(p)
			result, err := metadatafallback.New(f, f, f).ReadSeasonSupplement(t.Context(), 1396, 1, p)
			if result != nil || !errors.Is(err, ports.ErrBadResponse) || f.identityCalls.Load() != 0 {
				t.Fatalf("invalid base triggered network: %v", err)
			}
		})
	}
}
func TestSupplementMissingPrimaryAndIndependentEvidence(t *testing.T) {
	f := newFixture()
	date := time.Date(2008, 3, 9, 0, 0, 0, 0, time.UTC)
	f.matches[9002].Episode.AirDate = &date
	result, err := metadatafallback.New(f, f, f).ReadSeasonSupplement(t.Context(), 1396, 1, nil)
	if err != nil || result.Metadata.Complete || len(result.Metadata.Episodes) != 2 || len(result.Added) != 2 {
		t.Fatalf("missing primary fabricated completeness: %v", err)
	}
	*result.Added[1].Episode.AirDate = date.AddDate(1, 0, 0)
	if !result.Metadata.Episodes[1].AirDate.Equal(date) || !f.matches[9002].Episode.AirDate.Equal(date) {
		t.Fatal("source evidence aliases published metadata")
	}
	f.season.Episodes = nil
	empty, err := metadatafallback.New(f, f, f).ReadSeasonSupplement(t.Context(), 1396, 1, nil)
	if err != nil || empty.Metadata.Complete || len(empty.Metadata.Episodes) != 0 || len(empty.Added) != 0 {
		t.Fatal("empty source claimed completeness", err)
	}
}
func TestSupplementCancellationWaitsForPhysicalRead(t *testing.T) {
	f := newFixture()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.onResolve = func(context.Context, int64) (*ports.TVDBEpisodeResolution, error) {
		close(started)
		<-release
		return f.matches[9001], nil
	}
	done := make(chan error, 1)
	go func() {
		defer close(finished)
		result, err := metadatafallback.New(f, f, f).ReadSeasonSupplement(ctx, 1396, 1, primary())
		if result != nil {
			done <- errors.New("late observation published")
		} else {
			done <- err
		}
	}()
	t.Cleanup(func() {
		cancel()
		once.Do(func() { close(release) })
		select {
		case <-finished:
		case <-time.After(3 * time.Second):
			t.Error("physical read did not finish")
		}
	})
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("physical read not started")
	}
	cancel()
	select {
	case <-finished:
		t.Fatal("logical cancellation released active physical read")
	default:
	}
	once.Do(func() { close(release) })
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("late result accepted", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("read did not finish")
	}
	if f.resolveCalls.Load() != 1 {
		t.Fatal("cancelled traversal started another read")
	}
}
func TestSupplementFailureContractAndBackoff(t *testing.T) {
	for _, failure := range []error{fmt.Errorf("private-url: %w", ports.ErrUnavailable), errors.New("private-credential"), &ports.RateLimitError{RetryAfter: 19 * time.Second}} {
		f := newFixture()
		f.failure = failure
		result, err := metadatafallback.New(f, f, f).ReadSeasonSupplement(t.Context(), 1396, 1, primary())
		if result != nil || err == nil || strings.Contains(err.Error(), "private") || f.resolveCalls.Load() != 0 {
			t.Fatal("unsafe failure result", err)
		}
		var limited *ports.RateLimitError
		if errors.As(failure, &limited) {
			var got *ports.RateLimitError
			if !errors.As(err, &got) || got.RetryAfter != 19*time.Second {
				t.Fatal("backoff lost", err)
			}
		} else if !errors.Is(err, ports.ErrUnavailable) {
			t.Fatal("unsafe error classification", err)
		}
	}
}
