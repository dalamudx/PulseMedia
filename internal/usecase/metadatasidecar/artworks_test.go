package metadatasidecar_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/metadatasidecar"
)

type artsProvider struct {
	get   func(context.Context, ports.ArtworkIdentity) (ports.MediaArtworks, error)
	fetch func(context.Context, string) ([]byte, error)
}

func (p artsProvider) GetArtworks(ctx context.Context, id ports.ArtworkIdentity) (ports.MediaArtworks, error) {
	return p.get(ctx, id)
}
func (p artsProvider) FetchArtwork(ctx context.Context, path string) ([]byte, error) {
	return p.fetch(ctx, path)
}

type tvArtResolver func(context.Context, int64) (ports.TVArtworkIdentity, error)

func (f tvArtResolver) ResolveTVArtworkIdentity(ctx context.Context, id int64) (ports.TVArtworkIdentity, error) {
	return f(ctx, id)
}
func allArt(id ports.ArtworkIdentity) ports.MediaArtworks {
	return ports.MediaArtworks{Identity: id, ClearLogo: "logo", ClearArt: "clear", Banner: "banner", DiscArt: "disc"}
}
func artService(f *fixture, p ports.MediaArtworkProvider, tv ports.TVArtworkIdentityResolver) *metadatasidecar.Service {
	return metadatasidecar.NewService(f.repo, f.fs, nil, artwork(func(context.Context, string) ([]byte, error) { return []byte("poster"), nil }), metadatasidecar.WithArtworks(p, tv))
}

func TestArtworksBindIdentityAndSharePhysicalFences(t *testing.T) {
	for _, kind := range []string{"movie", "tv"} {
		t.Run(kind, func(t *testing.T) {
			snapshot := movieSnapshot()
			if kind == "tv" {
				snapshot = tvSnapshot()
			}
			snapshot.PosterPath = "/known.jpg"
			f := setup(t, snapshot)
			var requested ports.ArtworkIdentity
			var paths []string
			resolved := 0
			p := artsProvider{get: func(_ context.Context, id ports.ArtworkIdentity) (ports.MediaArtworks, error) {
				if f.fs.locked {
					t.Error("network under file lock")
				}
				requested = id
				return allArt(id), nil
			}, fetch: func(_ context.Context, path string) ([]byte, error) {
				if f.fs.locked {
					t.Error("download under file lock")
				}
				paths = append(paths, path)
				return []byte(path), nil
			}}
			tv := tvArtResolver(func(_ context.Context, id int64) (ports.TVArtworkIdentity, error) {
				resolved++
				return ports.TVArtworkIdentity{TMDBID: id, TVDBID: 81189}, nil
			})
			if err := artService(f, p, tv).Execute(f.ctx, f.lease); err != nil {
				t.Fatal(err)
			}
			wantID := ports.ArtworkIdentity{MediaType: kind, Provider: "tmdb", ID: 77}
			wantNames := []string{"movie.nfo", "poster.jpg", "logo.png", "clearart.png", "banner.jpg", "disc.png"}
			wantPaths := []string{"logo", "clear", "banner", "disc"}
			if kind == "tv" {
				wantID.Provider, wantID.ID = "tvdb", 81189
				wantNames = wantNames[:5]
				wantNames[0] = "tvshow.nfo"
				wantPaths = wantPaths[:3]
				if resolved != 1 {
					t.Fatal("TV resolver not used")
				}
			} else if resolved != 0 {
				t.Fatal("movie identity entered TV resolver")
			}
			names := []string{}
			for _, r := range f.fs.requests {
				names = append(names, r.Name)
				if r.DestinationDirectory != "Canonical (2024)" || r.SourceIdentity != snapshot.FileIdentity {
					t.Fatal("wrong destination/source binding")
				}
			}
			if requested != wantID || !reflect.DeepEqual(names, wantNames) || !reflect.DeepEqual(paths, wantPaths) || f.repo.completion == nil || len(f.repo.completion.Artifacts) != len(names) || !f.fs.released.Load() {
				t.Fatalf("wrong artifacts/namespace: %+v %v %v", requested, names, paths)
			}
		})
	}
}

func TestArtworkUnknownIdentityAndFailuresKeepNFOAndPoster(t *testing.T) {
	for _, scenario := range []string{"unavailable", "wrong_response_identity", "unknown_tv", "wrong_tmdb", "download_failure", "oversized", "rate_limit"} {
		t.Run(scenario, func(t *testing.T) {
			snapshot := movieSnapshot()
			if scenario == "unknown_tv" || scenario == "wrong_tmdb" {
				snapshot = tvSnapshot()
			}
			snapshot.PosterPath = "/known.jpg"
			f := setup(t, snapshot)
			gets, fetches := 0, 0
			p := artsProvider{get: func(_ context.Context, id ports.ArtworkIdentity) (ports.MediaArtworks, error) {
				gets++
				if scenario == "unavailable" {
					return ports.MediaArtworks{}, ports.ErrUnavailable
				}
				a := allArt(id)
				if scenario == "wrong_response_identity" {
					a.Identity.ID++
				}
				return a, nil
			}, fetch: func(context.Context, string) ([]byte, error) {
				fetches++
				if scenario == "oversized" {
					return make([]byte, ports.MaxSidecarBytes+1), nil
				}
				if scenario == "rate_limit" {
					return nil, ports.NewRateLimitError(time.Minute, "limited")
				}
				return nil, ports.ErrUnavailable
			}}
			tv := tvArtResolver(func(context.Context, int64) (ports.TVArtworkIdentity, error) {
				if scenario == "unknown_tv" {
					return ports.TVArtworkIdentity{}, ports.ErrNotFound
				}
				return ports.TVArtworkIdentity{TMDBID: 78, TVDBID: 81189}, nil
			})
			if err := artService(f, p, tv).Execute(f.ctx, f.lease); err != nil {
				t.Fatal(err)
			}
			if len(f.fs.requests) != 2 || f.fs.requests[1].Name != "poster.jpg" || f.repo.completion == nil {
				t.Fatal("optional failure suppressed legacy sidecars")
			}
			if (scenario == "unknown_tv" || scenario == "wrong_tmdb") && gets != 0 {
				t.Fatal("unproven TV identity queried Fanart")
			}
			if scenario == "wrong_response_identity" && fetches != 0 {
				t.Fatal("foreign images downloaded")
			}
			if scenario == "rate_limit" && fetches != 1 {
				t.Fatal("rate-limit ignored", fetches)
			}
		})
	}
}

func TestArtworkLateLeaseAndCancellationNeverWrite(t *testing.T) {
	for _, scenario := range []string{"stale_before", "stale_after", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			f := setup(t, movieSnapshot())
			ctx, cancel := context.WithCancel(f.ctx)
			defer cancel()
			f.ctx, f.repo.ctx = ctx, ctx
			gets := 0
			if scenario == "stale_before" {
				f.repo.validate = func(int, ports.MediaSidecarSnapshot) error { return ports.ErrMediaSidecarStale }
			}
			p := artsProvider{get: func(_ context.Context, id ports.ArtworkIdentity) (ports.MediaArtworks, error) {
				gets++
				return allArt(id), nil
			}, fetch: func(context.Context, string) ([]byte, error) {
				if scenario == "cancelled" {
					cancel()
				} else {
					f.repo.validate = func(int, ports.MediaSidecarSnapshot) error { return ports.ErrMediaSidecarStale }
				}
				return []byte("art"), nil
			}}
			err := artService(f, p, nil).Execute(f.ctx, f.lease)
			if !errors.Is(err, ports.ErrMediaSidecarStale) && !errors.Is(err, context.Canceled) {
				t.Fatal("late result accepted", err)
			}
			f.noEffects()
			if scenario == "stale_before" && gets != 0 {
				t.Fatal("stale job read network")
			}
		})
	}
}

func TestArtworkCancellationWaitsForNativeRead(t *testing.T) {
	f := setup(t, movieSnapshot())
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	f.ctx, f.repo.ctx = ctx, ctx
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	p := artsProvider{get: func(_ context.Context, id ports.ArtworkIdentity) (ports.MediaArtworks, error) { return allArt(id), nil }, fetch: func(context.Context, string) ([]byte, error) { close(entered); <-release; return []byte("art"), nil }}
	go func() { done <- artService(f, p, nil).Execute(f.ctx, f.lease) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("read not entered")
	}
	cancel()
	select {
	case <-done:
		t.Fatal("returned before physical read ended")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("read not joined")
	}
	f.noEffects()
}

func TestArtworkExistingAndUncertainResultsRemainObserved(t *testing.T) {
	for _, disposition := range []ports.SidecarDisposition{ports.SidecarPreservedRegular, ports.SidecarUnsafeExisting, ports.SidecarUncertain} {
		t.Run(string(disposition), func(t *testing.T) {
			f := setup(t, movieSnapshot())
			p := artsProvider{get: func(_ context.Context, id ports.ArtworkIdentity) (ports.MediaArtworks, error) { return allArt(id), nil }, fetch: func(context.Context, string) ([]byte, error) { return []byte("art"), nil }}
			f.fs.publish = func(r ports.SidecarRequest) (ports.SidecarResult, error) {
				out := created()
				out.Identity.Inode += int64(len(f.fs.requests))
				if r.Name != "logo.png" {
					return out, nil
				}
				out.Disposition = disposition
				out.Published = disposition == ports.SidecarUncertain
				if disposition == ports.SidecarUnsafeExisting {
					out.Identity = ports.FileIdentity{}
				}
				return out, nil
			}
			err := artService(f, p, nil).Execute(f.ctx, f.lease)
			if disposition == ports.SidecarPreservedRegular {
				if err != nil || len(f.fs.requests) != 5 {
					t.Fatal(err)
				}
			} else if !errors.Is(err, ports.ErrMediaSidecarResult) || len(f.fs.requests) != 2 {
				t.Fatal("unsafe result replayed", err)
			}
			if f.repo.completion == nil || f.repo.completion.Artifacts[1].Result.Disposition != disposition {
				t.Fatal("physical result lost")
			}
		})
	}
}
