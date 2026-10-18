package subscription_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
)

type creationCommandRepo struct {
	read                     func(context.Context, *ports.Actor, ports.SubscriptionCreationInput, ports.SubscriptionCreationIdentity) (ports.SubscriptionCreationContext, *ports.SubscriptionCreationReceipt, error)
	commit                   func(context.Context, *ports.Actor, ports.SubscriptionCreationInput, ports.SubscriptionCreationContext, *ports.PreparedSubscriptionCreationMetadata, ports.SubscriptionCreationIdentity, ports.SubscriptionCreationDecider) (*ports.SubscriptionCreationReceipt, error)
	receipt                  func(context.Context, *ports.Actor, string) (*ports.SubscriptionCreationReceipt, error)
	reads, commits, receipts int
}

func (r *creationCommandRepo) ReadSubscriptionCreationContext(ctx context.Context, a *ports.Actor, in ports.SubscriptionCreationInput, id ports.SubscriptionCreationIdentity) (ports.SubscriptionCreationContext, *ports.SubscriptionCreationReceipt, error) {
	r.reads++
	return r.read(ctx, a, in, id)
}
func (r *creationCommandRepo) CommitSubscriptionCreation(ctx context.Context, a *ports.Actor, in ports.SubscriptionCreationInput, f ports.SubscriptionCreationContext, m *ports.PreparedSubscriptionCreationMetadata, id ports.SubscriptionCreationIdentity, d ports.SubscriptionCreationDecider) (*ports.SubscriptionCreationReceipt, error) {
	r.commits++
	return r.commit(ctx, a, in, f, m, id, d)
}
func (r *creationCommandRepo) ReadSubscriptionCreationReceipt(ctx context.Context, a *ports.Actor, key string) (*ports.SubscriptionCreationReceipt, error) {
	r.receipts++
	return r.receipt(ctx, a, key)
}

type creationCommandProvider struct {
	ports.MetadataProvider
	movie func(context.Context, int64, string) (*ports.MovieMetadata, error)
	tv    func(context.Context, int64, string) (*ports.TVDetails, error)
}

func (p creationCommandProvider) GetMovieDetails(ctx context.Context, id int64, lang string) (*ports.MovieMetadata, error) {
	return p.movie(ctx, id, lang)
}
func (p creationCommandProvider) GetTVDetails(ctx context.Context, id int64, lang string) (*ports.TVDetails, error) {
	return p.tv(ctx, id, lang)
}

type creationCommandEpisodes func(context.Context, int64, int, string) (*ports.TVSeasonMetadata, error)

func (p creationCommandEpisodes) GetTVSeasonMetadata(ctx context.Context, id int64, s int, l string) (*ports.TVSeasonMetadata, error) {
	return p(ctx, id, s, l)
}

func ccPtr[T any](v T) *T { return &v }
func ccActor() *ports.Actor {
	return &ports.Actor{ID: uuid.NewString(), Type: ports.ActorTypeUser, Role: ports.RoleMember}
}
func ccProtector() ports.SecretProtector {
	return crypto.NewSecretProtector([]byte(strings.Repeat("k", 32)))
}
func ccContext(a *ports.Actor, typ string) ports.SubscriptionCreationContext {
	return ports.SubscriptionCreationContext{OwnerID: uuid.MustParse(a.ID), RootID: uuid.New(), MediaType: typ, TMDBID: 42, RootGeneration: 1, RootHealthRevision: 1, PolicyRevision: 3}
}
func ccReceipt(f ports.SubscriptionCreationContext) *ports.SubscriptionCreationReceipt {
	return &ports.SubscriptionCreationReceipt{Subscription: ports.SubscriptionRecord{ID: uuid.New(), UserID: f.OwnerID, MediaID: f.MediaID, RootID: f.RootID, State: "active", IntentVersion: 1}}
}
func ccUnit(season, episode int32, key string) ports.SubscriptionIntentUnit {
	return ports.SubscriptionIntentUnit{ID: uuid.New(), UnitKey: key, SeasonNumber: &season, EpisodeNumber: &episode}
}
func ccFrozen() ports.SubscriptionCreateContext {
	return ports.SubscriptionCreateContext{MediaID: uuid.New(), MediaType: "tv", RootMediaType: "tv", RootHealth: "online", PolicyRevision: 3, Units: []ports.SubscriptionIntentUnit{ccUnit(1, 1, "S01E01"), ccUnit(1, 3, "S01E03")}}
}
func ccInput(f ports.SubscriptionCreateContext) ports.SubscriptionCreationInput {
	return ports.SubscriptionCreationInput{MediaID: &f.MediaID}
}
func ccEqual[T any](t *testing.T, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v; want %#v", got, want)
	}
}

func TestSubscriptionCreationCommandNamespacesAndPublication(t *testing.T) {
	for _, typ := range []string{"movie", "tv"} {
		t.Run(typ, func(t *testing.T) {
			a := ccActor()
			f := ccContext(a, typ)
			calls := 0
			p := creationCommandProvider{movie: func(ctx context.Context, id int64, l string) (*ports.MovieMetadata, error) {
				calls++
				ccEqual(t, id, int64(42))
				if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 55*time.Second {
					t.Fatal("missing overall network budget")
				}
				return &ports.MovieMetadata{TMDBID: id, MediaType: "movie", Title: "Trusted", Year: 2026}, nil
			}, tv: func(ctx context.Context, id int64, l string) (*ports.TVDetails, error) {
				calls++
				return &ports.TVDetails{TMDBID: id, Title: "Trusted TV", Seasons: []ports.TVSeasonDetails{{SeasonNumber: 1, EpisodeCount: 2}}}, nil
			}}
			ep := creationCommandEpisodes(func(ctx context.Context, id int64, s int, l string) (*ports.TVSeasonMetadata, error) {
				calls++
				return &ports.TVSeasonMetadata{SeasonNumber: s, Complete: true, Episodes: []ports.TVEpisodeMetadata{{TMDBEpisodeID: 101, EpisodeNumber: 1}, {TMDBEpisodeID: 103, EpisodeNumber: 3}}}, nil
			})
			var readID ports.SubscriptionCreationIdentity
			r := &creationCommandRepo{}
			r.read = func(ctx context.Context, a *ports.Actor, in ports.SubscriptionCreationInput, id ports.SubscriptionCreationIdentity) (ports.SubscriptionCreationContext, *ports.SubscriptionCreationReceipt, error) {
				readID = id
				ccEqual(t, in.ScopeType, "all")
				return f, nil, nil
			}
			r.commit = func(ctx context.Context, a *ports.Actor, in ports.SubscriptionCreationInput, observed ports.SubscriptionCreationContext, m *ports.PreparedSubscriptionCreationMetadata, id ports.SubscriptionCreationIdentity, d ports.SubscriptionCreationDecider) (*ports.SubscriptionCreationReceipt, error) {
				ccEqual(t, id, readID)
				ccEqual(t, m.Media.Namespace, "tmdb."+typ)
				ccEqual(t, m.Media.ExternalID, "42")
				ccEqual(t, m.TMDBID, int64(42))
				if m.Media.Title == "" {
					t.Fatal("missing trusted title")
				}
				locked := ports.SubscriptionCreateContext{MediaID: uuid.New(), MediaType: typ, RootMediaType: typ, RootHealth: "online", PolicyRevision: 3}
				for _, u := range m.Units {
					locked.Units = append(locked.Units, ports.SubscriptionIntentUnit{ID: uuid.New(), UnitKey: u.UnitKey, SeasonNumber: u.SeasonNumber, EpisodeNumber: u.EpisodeNumber})
				}
				before := calls
				decision, err := d(locked)
				if err != nil {
					t.Fatal(err)
				}
				ccEqual(t, calls, before)
				ccEqual(t, len(decision.Wanted), len(m.Units))
				ccEqual(t, decision.Command.RootID, f.RootID)
				f.MediaID = locked.MediaID
				return ccReceipt(f), nil
			}
			in := ports.SubscriptionCreationInput{TMDBID: ccPtr(int64(42)), MediaType: typ}
			out, err := subscription.NewSubscriptionCreationService(r, p, ep, ccProtector()).Create(context.Background(), a, in, "create:42")
			if err != nil || out == nil {
				t.Fatalf("%v %v", out, err)
			}
			ccEqual(t, r.commits, 1)
			if typ == "movie" {
				ccEqual(t, calls, 1)
			} else {
				ccEqual(t, calls, 2)
			}
		})
	}
}

func TestSubscriptionCreationCommandRecoveryAndPrecheck(t *testing.T) {
	a := ccActor()
	f := ccContext(a, "tv")
	original := ccReceipt(f)
	original.Subscription.IndexerIDs = []uuid.UUID{uuid.New()}
	original.Wanted = []ports.SubscriptionCreationWanted{{ID: uuid.New()}}
	r := &creationCommandRepo{read: func(context.Context, *ports.Actor, ports.SubscriptionCreationInput, ports.SubscriptionCreationIdentity) (ports.SubscriptionCreationContext, *ports.SubscriptionCreationReceipt, error) {
		return f, original, nil
	}, receipt: func(context.Context, *ports.Actor, string) (*ports.SubscriptionCreationReceipt, error) {
		return original, nil
	}}
	s := subscription.NewSubscriptionCreationService(r, nil, nil, ccProtector())
	out, err := s.Create(context.Background(), a, ports.SubscriptionCreationInput{TMDBID: ccPtr(int64(42)), MediaType: "tv"}, "recover")
	if err != nil {
		t.Fatal(err)
	}
	ccEqual(t, out, original)
	ccEqual(t, r.commits, 0)
	out.Subscription.IndexerIDs[0] = uuid.Nil
	out.Wanted[0].ID = uuid.Nil
	if original.Subscription.IndexerIDs[0] == uuid.Nil || original.Wanted[0].ID == uuid.Nil {
		t.Fatal("receipt alias")
	}
	out, err = subscription.NewSubscriptionCreationService(r, nil, nil, nil).Receipt(context.Background(), a, "recover")
	if err != nil || out == nil {
		t.Fatal(err)
	}
	ccEqual(t, r.receipts, 1)
	r.read = func(context.Context, *ports.Actor, ports.SubscriptionCreationInput, ports.SubscriptionCreationIdentity) (ports.SubscriptionCreationContext, *ports.SubscriptionCreationReceipt, error) {
		return f, nil, ports.ErrForbidden
	}
	_, err = s.Create(context.Background(), a, ports.SubscriptionCreationInput{TMDBID: ccPtr(int64(42)), MediaType: "tv"}, "denied")
	ccEqual(t, err, ports.ErrForbidden)
	ccEqual(t, r.commits, 0)
}

func TestSubscriptionCreationCommandActorAndOwner(t *testing.T) {
	actor := ccActor()
	other := uuid.New()
	in := ports.SubscriptionCreationInput{TMDBID: ccPtr(int64(42))}
	cases := []struct {
		name  string
		a     *ports.Actor
		owner *uuid.UUID
		want  error
	}{
		{"nil", nil, nil, ports.ErrUnauthorized}, {"token", &ports.Actor{Type: ports.ActorTypeUser, ID: actor.ID, Role: ports.RoleAdmin, Scopes: []string{"admin"}}, nil, ports.ErrForbidden},
		{"empty-token", &ports.Actor{Type: ports.ActorTypeUser, ID: actor.ID, Role: ports.RoleAdmin, Scopes: []string{}}, nil, ports.ErrForbidden},
		{"system", &ports.Actor{Type: ports.ActorTypeSystem, ID: actor.ID, Role: ports.RoleAdmin}, nil, ports.ErrForbidden},
		{"bad-id", &ports.Actor{Type: ports.ActorTypeUser, ID: "bad", Role: ports.RoleMember}, nil, ports.ErrForbidden},
		{"nil-id", &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.Nil.String(), Role: ports.RoleMember}, nil, ports.ErrForbidden},
		{"wrong-role", &ports.Actor{Type: ports.ActorTypeUser, ID: actor.ID, Role: "viewer"}, nil, ports.ErrForbidden},
		{"member-other", actor, &other, ports.ErrForbidden}, {"member-self", actor, ccPtr(uuid.MustParse(actor.ID)), nil},
		{"admin-other", &ports.Actor{Type: ports.ActorTypeUser, ID: actor.ID, Role: ports.RoleAdmin}, &other, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &creationCommandRepo{read: func(ctx context.Context, a *ports.Actor, in ports.SubscriptionCreationInput, id ports.SubscriptionCreationIdentity) (ports.SubscriptionCreationContext, *ports.SubscriptionCreationReceipt, error) {
				f := ccContext(a, "movie")
				if in.UserID != nil {
					f.OwnerID = *in.UserID
				}
				return f, ccReceipt(f), nil
			}}
			r.receipt = func(context.Context, *ports.Actor, string) (*ports.SubscriptionCreationReceipt, error) {
				return ccReceipt(ccContext(actor, "movie")), nil
			}
			request := in
			request.UserID = tc.owner
			_, err := subscription.NewSubscriptionCreationService(r, nil, nil, ccProtector()).Create(context.Background(), tc.a, request, "actor")
			ccEqual(t, err, tc.want)
			if tc.want != nil {
				ccEqual(t, r.reads, 0)
			}
			_, err = subscription.NewSubscriptionCreationService(r, nil, nil, nil).Receipt(context.Background(), tc.a, "actor")
			if tc.name != "member-other" && tc.want != nil {
				ccEqual(t, err, tc.want)
			}
		})
	}
}

func TestSubscriptionCreationCommandCanonicalHMAC(t *testing.T) {
	a := ccActor()
	f := ccContext(a, "tv")
	idx1, idx2 := uuid.New(), uuid.New()
	u1, u2 := uuid.New(), uuid.New()
	input := ports.SubscriptionCreationInput{TMDBID: ccPtr(int64(42)), MediaType: "tv", ScopeType: "selected", Seasons: []int{3, 1}, EpisodeKeys: []string{"S03E03", "S01E01"}, SelectedUnitIDs: []uuid.UUID{u2, u1}, IndexerIDs: []uuid.UUID{idx1, idx2}}
	var canonical ports.SubscriptionCreationInput
	var identities []ports.SubscriptionCreationIdentity
	r := &creationCommandRepo{read: func(ctx context.Context, a *ports.Actor, in ports.SubscriptionCreationInput, id ports.SubscriptionCreationIdentity) (ports.SubscriptionCreationContext, *ports.SubscriptionCreationReceipt, error) {
		canonical = in
		identities = append(identities, id)
		return f, ccReceipt(f), nil
	}}
	s := subscription.NewSubscriptionCreationService(r, nil, nil, ccProtector())
	run := func(in ports.SubscriptionCreationInput) string {
		t.Helper()
		_, err := s.Create(context.Background(), a, in, "hash")
		if err != nil {
			t.Fatal(err)
		}
		return identities[len(identities)-1].RequestHash
	}
	h := run(input)
	ccEqual(t, input.Seasons, []int{3, 1})
	ccEqual(t, input.EpisodeKeys, []string{"S03E03", "S01E01"})
	raw, err := json.Marshal(canonical)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte(strings.Repeat("k", 32)))
	mac.Write(append([]byte("subscription.creation.v1:"), raw...))
	ccEqual(t, h, hex.EncodeToString(mac.Sum(nil)))
	reordered := input
	reordered.Seasons = []int{1, 3}
	reordered.EpisodeKeys = []string{"S01E01", "S03E03"}
	reordered.SelectedUnitIDs = []uuid.UUID{u1, u2}
	ccEqual(t, run(reordered), h)
	reordered.IndexerIDs = []uuid.UUID{idx2, idx1}
	if run(reordered) == h {
		t.Fatal("binding order disappeared")
	}
	reordered.IndexerIDs = nil
	nilHash := run(reordered)
	reordered.IndexerIDs = []uuid.UUID{}
	if run(reordered) == nilHash {
		t.Fatal("nil and empty indexers collapsed")
	}
	movie := ports.SubscriptionCreationInput{TMDBID: ccPtr(int64(42))}
	f.MediaType = "movie"
	implicit := run(movie)
	movie.MediaType = "movie"
	movie.ScopeType = "all"
	movie.QualityPolicyID = "default"
	ccEqual(t, run(movie), implicit)
	f.MediaType = "tv"
	movie.MediaType = "tv"
	if run(movie) == implicit {
		t.Fatal("movie and TV namespace collided")
	}
}

func TestSubscriptionCreationCommandInvalidInputs(t *testing.T) {
	a := ccActor()
	valid := ports.SubscriptionCreationInput{TMDBID: ccPtr(int64(42)), MediaType: "tv"}
	cases := []struct {
		name   string
		change func(*ports.SubscriptionCreationInput)
	}{
		{"identity-missing", func(i *ports.SubscriptionCreationInput) { i.TMDBID = nil }}, {"both-identities", func(i *ports.SubscriptionCreationInput) { i.MediaID = ccPtr(uuid.New()) }}, {"nil-media", func(i *ports.SubscriptionCreationInput) { i.TMDBID = nil; i.MediaID = ccPtr(uuid.Nil) }}, {"zero-tmdb", func(i *ports.SubscriptionCreationInput) { i.TMDBID = ccPtr(int64(0)) }},
		{"type", func(i *ports.SubscriptionCreationInput) { i.MediaType = "multi" }}, {"root", func(i *ports.SubscriptionCreationInput) { i.RootID = ccPtr(uuid.Nil) }}, {"owner", func(i *ports.SubscriptionCreationInput) { i.UserID = ccPtr(uuid.Nil) }}, {"downloader", func(i *ports.SubscriptionCreationInput) { i.DownloaderID = ccPtr(uuid.Nil) }},
		{"sources-bound", func(i *ports.SubscriptionCreationInput) { i.IndexerIDs = make([]uuid.UUID, 65) }}, {"source-duplicates", func(i *ports.SubscriptionCreationInput) { id := uuid.New(); i.IndexerIDs = []uuid.UUID{id, id} }}, {"seasons-bound", func(i *ports.SubscriptionCreationInput) { i.Seasons = make([]int, 65) }}, {"season-duplicates", func(i *ports.SubscriptionCreationInput) { i.Seasons = []int{1, 1} }}, {"season-range", func(i *ports.SubscriptionCreationInput) { i.Seasons = []int{1000} }}, {"special-implicit", func(i *ports.SubscriptionCreationInput) { i.Seasons = []int{0} }},
		{"all-episodes", func(i *ports.SubscriptionCreationInput) { i.EpisodeKeys = []string{"S01E01"} }}, {"empty-selected", func(i *ports.SubscriptionCreationInput) { i.ScopeType = "selected" }}, {"episode-duplicates", func(i *ports.SubscriptionCreationInput) {
			i.ScopeType = "selected"
			i.EpisodeKeys = []string{"S01E01", "S01E01"}
		}}, {"episode-noncanonical", func(i *ports.SubscriptionCreationInput) { i.ScopeType = "selected"; i.EpisodeKeys = []string{"S1E1"} }}, {"episode-overlong", func(i *ports.SubscriptionCreationInput) {
			i.ScopeType = "selected"
			i.EpisodeKeys = []string{strings.Repeat("x", 1000)}
		}}, {"episodes-bound", func(i *ports.SubscriptionCreationInput) {
			i.ScopeType = "selected"
			i.EpisodeKeys = make([]string, 20001)
		}},
		{"units-bound", func(i *ports.SubscriptionCreationInput) {
			i.ScopeType = "selected"
			i.SelectedUnitIDs = make([]uuid.UUID, 20001)
		}}, {"unit-duplicates", func(i *ports.SubscriptionCreationInput) {
			id := uuid.New()
			i.ScopeType = "selected"
			i.SelectedUnitIDs = []uuid.UUID{id, id}
		}}, {"version", func(i *ports.SubscriptionCreationInput) { i.ExpectedIntentVersion = ccPtr(int64(0)) }}, {"movie-seasons", func(i *ports.SubscriptionCreationInput) { i.MediaType = "movie"; i.Seasons = []int{1} }}, {"movie-specials", func(i *ports.SubscriptionCreationInput) { i.MediaType = "movie"; i.IncludeSpecials = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &creationCommandRepo{}
			in := valid
			tc.change(&in)
			_, err := subscription.NewSubscriptionCreationService(r, nil, nil, ccProtector()).Create(context.Background(), a, in, "invalid")
			if err != ports.ErrInvalidSubscriptionBindings {
				t.Fatalf("got %v", err)
			}
			ccEqual(t, r.reads, 0)
		})
	}
	for _, key := range []string{"", strings.Repeat("k", 161), "a b", "a/b", "é", "x\n"} {
		r := &creationCommandRepo{}
		_, err := subscription.NewSubscriptionCreationService(r, nil, nil, ccProtector()).Create(context.Background(), a, valid, key)
		ccEqual(t, err, ports.ErrInvalidSubscriptionBindings)
		ccEqual(t, r.reads, 0)
	}
	_, err := subscription.NewSubscriptionCreationService(&creationCommandRepo{}, nil, nil, nil).Create(context.Background(), a, valid, "valid")
	ccEqual(t, err, ports.ErrUnavailable)
}

func TestSubscriptionCreationCommandUUIDInferenceAndContextValidation(t *testing.T) {
	for _, typ := range []string{"movie", "tv"} {
		t.Run(typ, func(t *testing.T) {
			a := ccActor()
			f := ccContext(a, typ)
			f.MediaID = uuid.New()
			f.MediaVersion = 2
			f.TMDBID = 0
			r := &creationCommandRepo{read: func(context.Context, *ports.Actor, ports.SubscriptionCreationInput, ports.SubscriptionCreationIdentity) (ports.SubscriptionCreationContext, *ports.SubscriptionCreationReceipt, error) {
				return f, nil, nil
			}, commit: func(ctx context.Context, a *ports.Actor, in ports.SubscriptionCreationInput, f ports.SubscriptionCreationContext, m *ports.PreparedSubscriptionCreationMetadata, id ports.SubscriptionCreationIdentity, d ports.SubscriptionCreationDecider) (*ports.SubscriptionCreationReceipt, error) {
				if m != nil {
					t.Fatal("legacy UUID forced network")
				}
				return ccReceipt(f), nil
			}}
			_, err := subscription.NewSubscriptionCreationService(r, nil, nil, ccProtector()).Create(context.Background(), a, ports.SubscriptionCreationInput{MediaID: &f.MediaID}, "uuid")
			if err != nil {
				t.Fatal(err)
			}
			ccEqual(t, r.commits, 1)
		})
	}
	cases := []struct {
		name   string
		change func(*ports.SubscriptionCreationContext)
		want   error
	}{
		{"owner", func(f *ports.SubscriptionCreationContext) { f.OwnerID = uuid.New() }, ports.ErrForbidden}, {"root", func(f *ports.SubscriptionCreationContext) { f.RootID = uuid.Nil }, ports.ErrInvalidSubscriptionBindings}, {"type", func(f *ports.SubscriptionCreationContext) { f.MediaType = "movie" }, ports.ErrInvalidSubscriptionBindings}, {"identity", func(f *ports.SubscriptionCreationContext) { f.TMDBID = 43 }, ports.ErrSubscriptionBindingsConflict}, {"version", func(f *ports.SubscriptionCreationContext) { f.MediaID = uuid.New() }, ports.ErrSubscriptionBindingsConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := ccActor()
			f := ccContext(a, "tv")
			tc.change(&f)
			r := &creationCommandRepo{read: func(context.Context, *ports.Actor, ports.SubscriptionCreationInput, ports.SubscriptionCreationIdentity) (ports.SubscriptionCreationContext, *ports.SubscriptionCreationReceipt, error) {
				return f, nil, nil
			}}
			_, err := subscription.NewSubscriptionCreationService(r, nil, nil, ccProtector()).Create(context.Background(), a, ports.SubscriptionCreationInput{TMDBID: ccPtr(int64(42)), MediaType: "tv"}, "bad-context")
			ccEqual(t, err, tc.want)
			ccEqual(t, r.commits, 0)
		})
	}
}

func TestSubscriptionCreationCommandNetworkFailuresAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name          string
		providerError error
		cancel        bool
		want          error
	}{
		{"unknown", errors.New("https://secret:pass@provider/candidate"), false, ports.ErrUnavailable}, {"bad-response", fmtWrappedBadResponse(), false, ports.ErrBadResponse}, {"canceled-late-success", nil, true, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			a := ccActor()
			f := ccContext(a, "movie")
			r := &creationCommandRepo{read: func(context.Context, *ports.Actor, ports.SubscriptionCreationInput, ports.SubscriptionCreationIdentity) (ports.SubscriptionCreationContext, *ports.SubscriptionCreationReceipt, error) {
				return f, nil, nil
			}}
			p := creationCommandProvider{movie: func(context.Context, int64, string) (*ports.MovieMetadata, error) {
				if tc.cancel {
					cancel()
				}
				return &ports.MovieMetadata{TMDBID: 42, MediaType: "movie", Title: "Trusted"}, tc.providerError
			}}
			_, err := subscription.NewSubscriptionCreationService(r, p, nil, ccProtector()).Create(ctx, a, ports.SubscriptionCreationInput{TMDBID: ccPtr(int64(42))}, "network")
			ccEqual(t, err, tc.want)
			ccEqual(t, r.commits, 0)
		})
	}
}
func fmtWrappedBadResponse() error {
	return errors.Join(errors.New("sensitive provider body"), ports.ErrBadResponse)
}

func TestSubscriptionCreationCommandClonesAndPureCallback(t *testing.T) {
	a := ccActor()
	f := ccContext(a, "tv")
	f.MediaID = uuid.New()
	f.MediaVersion = 1
	indexer, downloader := uuid.New(), uuid.New()
	input := ports.SubscriptionCreationInput{MediaID: ccPtr(f.MediaID), Seasons: []int{1}, IndexerIDs: []uuid.UUID{indexer}, DownloaderID: &downloader}
	date := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	absolute := 3
	upstream := &ports.TVSeasonMetadata{SeasonNumber: 1, Complete: true, Episodes: []ports.TVEpisodeMetadata{{TMDBEpisodeID: 103, EpisodeNumber: 3, AirDate: &date, AbsoluteNumber: &absolute}}}
	p := creationCommandProvider{tv: func(context.Context, int64, string) (*ports.TVDetails, error) {
		input.Seasons[0] = 2
		*input.MediaID = uuid.New()
		input.IndexerIDs[0] = uuid.Nil
		downloader = uuid.Nil
		return &ports.TVDetails{TMDBID: 42, Title: "Trusted", Seasons: []ports.TVSeasonDetails{{SeasonNumber: 1, EpisodeCount: 1}}}, nil
	}}
	ep := creationCommandEpisodes(func(context.Context, int64, int, string) (*ports.TVSeasonMetadata, error) { return upstream, nil })
	r := &creationCommandRepo{}
	r.read = func(ctx context.Context, a *ports.Actor, in ports.SubscriptionCreationInput, id ports.SubscriptionCreationIdentity) (ports.SubscriptionCreationContext, *ports.SubscriptionCreationReceipt, error) {
		in.Seasons[0] = 3
		*in.MediaID = uuid.Nil
		in.IndexerIDs[0] = uuid.Nil
		*in.DownloaderID = uuid.Nil
		return f, nil, nil
	}
	r.commit = func(ctx context.Context, a *ports.Actor, in ports.SubscriptionCreationInput, observed ports.SubscriptionCreationContext, m *ports.PreparedSubscriptionCreationMetadata, id ports.SubscriptionCreationIdentity, d ports.SubscriptionCreationDecider) (*ports.SubscriptionCreationReceipt, error) {
		ccEqual(t, in.Seasons, []int{1})
		ccEqual(t, in.IndexerIDs, []uuid.UUID{indexer})
		if *in.DownloaderID == uuid.Nil {
			t.Fatal("caller aliased downloader")
		}
		in.Seasons[0] = 9
		in.IndexerIDs[0] = uuid.Nil
		*in.MediaID = uuid.Nil
		*in.DownloaderID = uuid.Nil
		unit := ccUnit(1, 3, "S01E03")
		locked := ports.SubscriptionCreateContext{MediaID: f.MediaID, MediaType: "tv", RootMediaType: "tv", RootHealth: "online", PolicyRevision: 3, Units: []ports.SubscriptionIntentUnit{unit}}
		before := *locked.Units[0].SeasonNumber
		decision, err := d(locked)
		if err != nil {
			t.Fatal(err)
		}
		ccEqual(t, decision.Command.Seasons, []int{1})
		ccEqual(t, decision.Command.IndexerIDs, []uuid.UUID{indexer})
		decision.Command.Seasons[0] = 8
		decision.Command.IndexerIDs[0] = uuid.Nil
		*decision.Command.DownloaderID = uuid.Nil
		again, err := d(locked)
		if err != nil {
			t.Fatal(err)
		}
		ccEqual(t, again.Command.Seasons, []int{1})
		ccEqual(t, again.Command.IndexerIDs, []uuid.UUID{indexer})
		ccEqual(t, *locked.Units[0].SeasonNumber, before)
		*m.Seasons[0].Episodes[0].AirDate = time.Time{}
		*m.Seasons[0].Episodes[0].AbsoluteNumber = 99
		*m.Units[0].SeasonNumber = 9
		if upstream.Episodes[0].AirDate.IsZero() || absolute != 3 {
			t.Fatal("provider metadata alias")
		}
		return ccReceipt(f), nil
	}
	_, err := subscription.NewSubscriptionCreationService(r, p, ep, ccProtector()).Create(context.Background(), a, input, "clone")
	if err != nil {
		t.Fatal(err)
	}
}

func TestDecideSubscriptionCreationScope(t *testing.T) {
	f := ccFrozen()
	root := uuid.New()
	special := ccUnit(0, 1, "S00E01")
	retired := ccUnit(1, 2, "S01E02")
	retired.Retired = true
	unknown := ports.SubscriptionIntentUnit{ID: uuid.New(), UnitKey: "S01E04"}
	season2 := ccUnit(2, 1, "S02E01")
	f.Units = append(f.Units, special, retired, unknown, season2)
	cases := []struct {
		name             string
		alter            func(*ports.SubscriptionCreationInput)
		wantErr          bool
		wanted, selected int
	}{
		{"all", func(*ports.SubscriptionCreationInput) {}, false, 3, 0}, {"season", func(i *ports.SubscriptionCreationInput) { i.Seasons = []int{1} }, false, 2, 0}, {"specials", func(i *ports.SubscriptionCreationInput) { i.IncludeSpecials = true }, false, 4, 0},
		{"selected-sparse", func(i *ports.SubscriptionCreationInput) { i.ScopeType = "selected"; i.EpisodeKeys = []string{"S01E03"} }, false, 3, 1}, {"selected-uuid", func(i *ports.SubscriptionCreationInput) {
			i.ScopeType = "selected"
			i.SelectedUnitIDs = []uuid.UUID{f.Units[0].ID}
		}, false, 3, 1},
		{"missing-sparse", func(i *ports.SubscriptionCreationInput) { i.ScopeType = "selected"; i.EpisodeKeys = []string{"S01E02"} }, true, 0, 0}, {"unknown", func(i *ports.SubscriptionCreationInput) { i.ScopeType = "selected"; i.EpisodeKeys = []string{"S01E04"} }, true, 0, 0}, {"retired", func(i *ports.SubscriptionCreationInput) {
			i.ScopeType = "selected"
			i.SelectedUnitIDs = []uuid.UUID{retired.ID}
		}, true, 0, 0},
		{"implicit-special", func(i *ports.SubscriptionCreationInput) { i.ScopeType = "selected"; i.EpisodeKeys = []string{"S00E01"} }, true, 0, 0}, {"explicit-special", func(i *ports.SubscriptionCreationInput) {
			i.ScopeType = "selected"
			i.EpisodeKeys = []string{"S00E01"}
			i.IncludeSpecials = true
		}, false, 4, 1}, {"selected-outside-season", func(i *ports.SubscriptionCreationInput) {
			i.ScopeType = "selected"
			i.Seasons = []int{2}
			i.EpisodeKeys = []string{"S01E01"}
		}, true, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := ccInput(f)
			tc.alter(&in)
			before, _ := json.Marshal(f)
			out, err := subscription.DecideSubscriptionCreation(in, root, f)
			if tc.wantErr {
				ccEqual(t, err, ports.ErrInvalidSubscriptionBindings)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			ccEqual(t, len(out.Wanted), tc.wanted)
			ccEqual(t, len(out.Command.SelectedUnitIDs), tc.selected)
			after, _ := json.Marshal(f)
			ccEqual(t, string(after), string(before))
			for _, w := range out.Wanted {
				if out.Command.ScopeType == "selected" {
					ccEqual(t, w.ScopeExcluded, !slices.Contains(out.Command.SelectedUnitIDs, w.UnitID))
				}
			}
		})
	}
	f.Units = nil
	out, err := subscription.DecideSubscriptionCreation(ccInput(f), root, f)
	if err != nil {
		t.Fatal(err)
	}
	ccEqual(t, len(out.Wanted), 0)
	f.MediaType = "movie"
	f.RootMediaType = "movie"
	_, err = subscription.DecideSubscriptionCreation(ccInput(f), root, f)
	ccEqual(t, err, ports.ErrInvalidSubscriptionBindings)
}

func TestDecideSubscriptionCreationEnsureAndExpectedVersion(t *testing.T) {
	f := ccFrozen()
	root := uuid.New()
	indexer, downloader := uuid.New(), uuid.New()
	existing := ports.SubscriptionRecord{ID: uuid.New(), UserID: uuid.New(), MediaID: f.MediaID, RootID: root, QualityPolicyID: "default", QualityPolicyRevision: 2, IndexerIDs: []uuid.UUID{indexer}, DownloaderID: &downloader, State: "paused", IntentVersion: 7, ScopeType: "all", Seasons: []int{1}}
	f.Existing = &existing
	in := ccInput(f)
	in.Seasons = []int{1}
	out, err := subscription.DecideSubscriptionCreation(in, root, f)
	if err != nil {
		t.Fatal(err)
	}
	ccEqual(t, out.Command.QualityPolicyRevision, int64(2))
	ccEqual(t, out.Command.IndexerIDs, []uuid.UUID{indexer})
	ccEqual(t, *out.Command.DownloaderID, downloader)
	out.Command.IndexerIDs[0] = uuid.Nil
	*out.Command.DownloaderID = uuid.Nil
	ccEqual(t, existing.IndexerIDs, []uuid.UUID{indexer})
	if downloader == uuid.Nil {
		t.Fatal("inherited pointer alias")
	}
	ccEqual(t, existing.State, "paused")
	for _, tc := range []struct {
		name   string
		change func(*ports.SubscriptionCreationInput)
	}{
		{"default-scope-explicit", func(i *ports.SubscriptionCreationInput) { i.Seasons = nil }}, {"disable-indexers", func(i *ports.SubscriptionCreationInput) { i.IndexerIDs = []uuid.UUID{} }}, {"change-policy", func(i *ports.SubscriptionCreationInput) { i.QualityPolicyID = "new" }}, {"change-downloader", func(i *ports.SubscriptionCreationInput) { i.DownloaderID = ccPtr(uuid.New()) }}, {"change-specials", func(i *ports.SubscriptionCreationInput) { i.IncludeSpecials = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := in
			tc.change(&request)
			_, err := subscription.DecideSubscriptionCreation(request, root, f)
			ccEqual(t, err, ports.ErrSubscriptionBindingsConflict)
			request.ExpectedIntentVersion = ccPtr(int64(7))
			out, err := subscription.DecideSubscriptionCreation(request, root, f)
			if err != nil {
				t.Fatal(err)
			}
			ccEqual(t, out.Command.QualityPolicyRevision, int64(3))
		})
	}
	in.ExpectedIntentVersion = ccPtr(int64(6))
	_, err = subscription.DecideSubscriptionCreation(in, root, f)
	ccEqual(t, err, ports.ErrSubscriptionBindingsConflict)
	f.Existing = nil
	in.ExpectedIntentVersion = ccPtr(int64(7))
	_, err = subscription.DecideSubscriptionCreation(in, root, f)
	ccEqual(t, err, ports.ErrSubscriptionBindingsConflict)
	f.Existing = &existing
	existing.State = "archived"
	existing.ScopeType = "selected"
	existing.SelectedUnitIDs = []uuid.UUID{f.Units[1].ID}
	in.ExpectedIntentVersion = nil
	in.ScopeType = "selected"
	in.EpisodeKeys = []string{"S01E03"}
	_, err = subscription.DecideSubscriptionCreation(in, root, f)
	if err != nil {
		t.Fatal(err)
	}
	ccEqual(t, existing.State, "archived")
}

func TestSubscriptionCreationCommandHistoricalRecoveryWithoutObservation(t *testing.T) {
	a := ccActor()
	f := ccContext(a, "tv")
	f.MediaID = uuid.New()
	saved := ccReceipt(f)
	r := &creationCommandRepo{read: func(context.Context, *ports.Actor, ports.SubscriptionCreationInput, ports.SubscriptionCreationIdentity) (ports.SubscriptionCreationContext, *ports.SubscriptionCreationReceipt, error) {
		return ports.SubscriptionCreationContext{}, saved, nil
	}}
	// Current version can have advanced: a recovered result retains its original
	// version and does not run new preconditions or require metadata dependencies.
	in := ports.SubscriptionCreationInput{MediaID: &f.MediaID, ExpectedIntentVersion: ccPtr(int64(99))}
	got, err := subscription.NewSubscriptionCreationService(r, nil, nil, ccProtector()).Create(context.Background(), a, in, "original")
	if err != nil {
		t.Fatal(err)
	}
	ccEqual(t, got, saved)
	ccEqual(t, r.commits, 0)
	saved.Subscription.UserID = uuid.New()
	_, err = subscription.NewSubscriptionCreationService(r, nil, nil, ccProtector()).Create(context.Background(), a, in, "original")
	ccEqual(t, err, ports.ErrForbidden)
}

func TestSubscriptionCreationCommandStaleVersionBeforeMetadata(t *testing.T) {
	for _, hasExisting := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "stale"}[hasExisting], func(t *testing.T) {
			a := ccActor()
			f := ccContext(a, "tv")
			if hasExisting {
				f.MediaID = uuid.New()
				f.MediaVersion = 1
				f.Existing = &ports.SubscriptionRecord{ID: uuid.New(), UserID: f.OwnerID, MediaID: f.MediaID, RootID: f.RootID, IntentVersion: 2}
			}
			r := &creationCommandRepo{read: func(context.Context, *ports.Actor, ports.SubscriptionCreationInput, ports.SubscriptionCreationIdentity) (ports.SubscriptionCreationContext, *ports.SubscriptionCreationReceipt, error) {
				return f, nil, nil
			}}
			_, err := subscription.NewSubscriptionCreationService(r, nil, nil, ccProtector()).Create(context.Background(), a, ports.SubscriptionCreationInput{TMDBID: ccPtr(int64(42)), MediaType: "tv", ExpectedIntentVersion: ccPtr(int64(1))}, "stale")
			ccEqual(t, err, ports.ErrSubscriptionBindingsConflict)
			ccEqual(t, r.commits, 0)
		})
	}
}

type creationCommandBadProtector struct{ ports.SecretProtector }

func (creationCommandBadProtector) DigestSecret([]byte) (string, error) {
	return "", errors.New("secret digest failure")
}

func TestSubscriptionCreationCommandSafeBoundaryErrors(t *testing.T) {
	a := ccActor()
	f := ccContext(a, "movie")
	in := ports.SubscriptionCreationInput{TMDBID: ccPtr(int64(42))}
	sensitive := errors.New("raw-provider-candidate-and-secret")
	r := &creationCommandRepo{read: func(context.Context, *ports.Actor, ports.SubscriptionCreationInput, ports.SubscriptionCreationIdentity) (ports.SubscriptionCreationContext, *ports.SubscriptionCreationReceipt, error) {
		return f, nil, sensitive
	}, receipt: func(context.Context, *ports.Actor, string) (*ports.SubscriptionCreationReceipt, error) {
		return nil, sensitive
	}}
	s := subscription.NewSubscriptionCreationService(r, nil, nil, ccProtector())
	_, err := s.Create(context.Background(), a, in, "read-error")
	ccEqual(t, err, ports.ErrUnavailable)
	_, err = s.Receipt(context.Background(), a, "receipt-error")
	ccEqual(t, err, ports.ErrUnavailable)
	reads := r.reads
	_, err = subscription.NewSubscriptionCreationService(r, nil, nil, creationCommandBadProtector{}).Create(context.Background(), a, in, "digest-error")
	ccEqual(t, err, ports.ErrUnavailable)
	ccEqual(t, r.reads, reads)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = s.Create(ctx, a, in, "canceled")
	ccEqual(t, err, context.Canceled)
	ccEqual(t, r.reads, reads)
	before := r.receipts
	_, err = s.Receipt(ctx, a, "canceled")
	ccEqual(t, err, context.Canceled)
	ccEqual(t, r.receipts, before)
	r.read = func(context.Context, *ports.Actor, ports.SubscriptionCreationInput, ports.SubscriptionCreationIdentity) (ports.SubscriptionCreationContext, *ports.SubscriptionCreationReceipt, error) {
		return f, nil, nil
	}
	r.commit = func(context.Context, *ports.Actor, ports.SubscriptionCreationInput, ports.SubscriptionCreationContext, *ports.PreparedSubscriptionCreationMetadata, ports.SubscriptionCreationIdentity, ports.SubscriptionCreationDecider) (*ports.SubscriptionCreationReceipt, error) {
		return nil, sensitive
	}
	p := creationCommandProvider{movie: func(context.Context, int64, string) (*ports.MovieMetadata, error) {
		return &ports.MovieMetadata{TMDBID: 42, MediaType: "movie", Title: "Trusted"}, nil
	}}
	_, err = subscription.NewSubscriptionCreationService(r, p, nil, ccProtector()).Create(context.Background(), a, in, "commit-error")
	ccEqual(t, err, ports.ErrUnavailable)
}

func TestDecideSubscriptionCreationConcurrentOwnedResults(t *testing.T) {
	frozen := ccFrozen()
	root := uuid.New()
	in := ccInput(frozen)
	in.ScopeType = "selected"
	in.EpisodeKeys = []string{"S01E03"}
	in.IndexerIDs = []uuid.UUID{uuid.New()}
	result := make(chan error, 8)
	for range 8 {
		go func() {
			for range 20 {
				out, err := subscription.DecideSubscriptionCreation(in, root, frozen)
				if err != nil {
					result <- err
					return
				}
				out.Command.IndexerIDs[0] = uuid.Nil
				out.Command.SelectedUnitIDs[0] = uuid.Nil
				out.Command.EpisodeKeys[0] = "changed"
				out.Wanted[0].UnitID = uuid.Nil
			}
			result <- nil
		}()
	}
	for range 8 {
		if err := <-result; err != nil {
			t.Error(err)
		}
	}
	ccEqual(t, in.EpisodeKeys, []string{"S01E03"})
	if in.IndexerIDs[0] == uuid.Nil || frozen.Units[0].ID == uuid.Nil {
		t.Fatal("pure results aliased shared input")
	}
}
