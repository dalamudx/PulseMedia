package postgres_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
)

// Embedding supplies the unused provider methods; unexpected calls fail the test.
type artworkCreationProvider struct {
	ports.MetadataProvider
	calls  atomic.Int32
	poster string
}

func (p *artworkCreationProvider) GetMovieDetails(_ context.Context, id int64, _ string) (*ports.MovieMetadata, error) {
	p.calls.Add(1)
	return &ports.MovieMetadata{TMDBID: id, MediaType: "movie", Title: "Trusted Movie", PosterURL: p.poster}, nil
}
func (p *artworkCreationProvider) GetTVDetails(_ context.Context, id int64, _ string) (*ports.TVDetails, error) {
	p.calls.Add(1)
	return &ports.TVDetails{TMDBID: id, Title: "Trusted TV", PosterURL: p.poster, Seasons: []ports.TVSeasonDetails{{SeasonNumber: 1, EpisodeCount: 2}}}, nil
}
func (p *artworkCreationProvider) GetTVSeasonMetadata(_ context.Context, _ int64, _ int, _ string) (*ports.TVSeasonMetadata, error) {
	p.calls.Add(1)
	s := creationSeason(1, 1, 3)
	return &s, nil
}

type artworkCreationSecrets struct{}

func (artworkCreationSecrets) DigestSecret(raw []byte) (string, error) {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func (artworkCreationSecrets) EncryptSecret([]byte) ([]byte, error) {
	return nil, errors.New("unexpected encryption")
}

func creationArtworkSnapshot(t *testing.T, f creationFixture) string {
	t.Helper()
	var artwork string
	if err := f.pool.QueryRow(t.Context(), `SELECT coalesce(jsonb_agg(to_jsonb(a) ORDER BY media_id),'[]')::text FROM media_artwork a`).Scan(&artwork); err != nil {
		t.Fatal(err)
	}
	return f.snapshot(t) + artwork
}

func requireCreationArtwork(t *testing.T, f creationFixture, id uuid.UUID, namespace, externalID, path string) {
	t.Helper()
	var gotNamespace, gotID, gotPath, provider string
	var version, current int64
	if err := f.pool.QueryRow(t.Context(), `SELECT a.provider,a.namespace,a.external_id,a.poster_path,a.metadata_version,m.metadata_version FROM media_artwork a JOIN media m ON m.id=a.media_id WHERE m.id=$1`, id).Scan(&provider, &gotNamespace, &gotID, &gotPath, &version, &current); err != nil {
		t.Fatal(err)
	}
	if provider != "tmdb" || gotNamespace != namespace || gotID != externalID || gotPath != path || version != current {
		t.Fatalf("artwork provider=%q namespace=%q id=%q path=%q version=%d current=%d", provider, gotNamespace, gotID, gotPath, version, current)
	}
}

func TestCreationArtworkRequiresOneExactNamespaceIdentity(t *testing.T) {
	f := newCreationFixture(t, 92)
	in, p := creationInput(6211, "movie"), creationMovie(6211)
	r := f.create(t, in, p, "identity-seed")
	if _, err := db.New(f.pool).CreateMediaIdentity(t.Context(), db.CreateMediaIdentityParams{ID: uuid.New(), MediaID: r.Subscription.MediaID, Namespace: "tmdb.movie", ExternalID: "6212"}); err != nil {
		t.Fatal(err)
	}
	p.PosterPath = "/ambiguous.jpg"
	id := creationIdentity("ambiguous-artwork")
	h := f.read(t, in, id)
	before := creationArtworkSnapshot(t, f)
	_, err := f.store.CommitSubscriptionCreation(t.Context(), f.actor, in, h, p, id, creationDecider(in, h))
	if !errors.Is(err, ports.ErrSubscriptionBindingsConflict) || before != creationArtworkSnapshot(t, f) {
		t.Fatal("ambiguous namespace identity published", err)
	}
}

func TestCreationArtworkNativeServiceAndReplay(t *testing.T) {
	f := newCreationFixture(t, 117)
	p := &artworkCreationProvider{poster: "https://image.tmdb.org/t/p/original/Trusted_09-poster.jpg"}
	svc := subscription.NewSubscriptionCreationService(f.store, p, p, artworkCreationSecrets{})
	mediaIDs := map[string]uuid.UUID{}
	for _, kind := range []string{"movie", "tv"} {
		in := creationInput(6201, kind)
		key := "artwork-native-" + kind
		first, err := svc.Create(t.Context(), f.actor, in, key)
		if err != nil {
			t.Fatal(err)
		}
		mediaIDs[kind] = first.Subscription.MediaID
		requireCreationArtwork(t, f, first.Subscription.MediaID, "tmdb."+kind, "6201", "/Trusted_09-poster.jpg")
		before, calls := creationArtworkSnapshot(t, f), p.calls.Load()
		p.poster = "https://image.tmdb.org/t/p/w500/changed.jpg"
		replay, err := svc.Create(t.Context(), f.actor, in, key)
		if err != nil || !reflect.DeepEqual(first, replay) || before != creationArtworkSnapshot(t, f) || calls != p.calls.Load() {
			t.Fatalf("replay changed receipt, network calls or database: %v", err)
		}
		p.poster = "https://image.tmdb.org/t/p/original/Trusted_09-poster.jpg"
	}
	if mediaIDs["movie"] == mediaIDs["tv"] || p.calls.Load() != 3 {
		t.Fatal("native namespace/call counts", mediaIDs, p.calls.Load())
	}
}

func TestCreationArtworkInvalidPathRollsBack(t *testing.T) {
	for _, path := range []string{"https://image.tmdb.org/t/p/w342/a.jpg", "/../a.jpg", "/a.jpg?secret=x", "/a.jpg#x", "/%61.jpg", "/a/b.jpg", "/a\\b.jpg", "/a.svg", " /a.jpg"} {
		t.Run(path, func(t *testing.T) {
			f := newCreationFixture(t, 92)
			in, p := creationInput(6202, "movie"), creationMovie(6202)
			p.PosterPath = path
			id := creationIdentity("invalid-artwork")
			h := f.read(t, in, id)
			before := creationArtworkSnapshot(t, f)
			_, err := f.store.CommitSubscriptionCreation(t.Context(), f.actor, in, h, p, id, creationDecider(in, h))
			if !errors.Is(err, ports.ErrInvalidSubscriptionBindings) || before != creationArtworkSnapshot(t, f) {
				t.Fatal("invalid artwork escaped rollback", err)
			}
		})
	}
}

func TestCreationArtworkEmptyHistoricalAndUnknown(t *testing.T) {
	// No media_artwork table exists at this historical boundary.
	old := newCreationFixture(t, 91)
	in := creationInput(6203, "movie")
	old.create(t, in, creationMovie(6203), "historical-empty")
	old.create(t, in, nil, "historical-nil")
	for _, kind := range []string{"movie", "tv"} {
		f := newCreationFixture(t, 117)
		in := creationInput(6204, kind)
		p := creationMovie(6204)
		if kind == "tv" {
			p = creationTV(6204, creationSeason(1, 1, 3))
		}
		r := f.create(t, in, p, "unknown-empty")
		f.create(t, in, nil, "unknown-nil")
		if n := intentCount(t, f.pool, "SELECT count(*) FROM media_artwork"); n != 0 {
			t.Fatal("missing artwork became known", kind, n)
		}
		p.PosterPath = "/known.jpg"
		f.create(t, in, p, "known")
		var before string
		if err := f.pool.QueryRow(t.Context(), "SELECT to_jsonb(a)::text FROM media_artwork a").Scan(&before); err != nil {
			t.Fatal(err)
		}
		p.PosterPath = ""
		f.create(t, in, p, "preserve-empty")
		f.create(t, in, nil, "preserve-nil")
		var after string
		if err := f.pool.QueryRow(t.Context(), "SELECT to_jsonb(a)::text FROM media_artwork a WHERE media_id=$1", r.Subscription.MediaID).Scan(&after); err != nil || before != after {
			t.Fatal("empty observation rewrote cache", kind, err)
		}
	}
}

func TestCreationArtworkTrustedPublicationReplacesFutureCache(t *testing.T) {
	f := newCreationFixture(t, 92)
	in, p := creationInput(6205, "movie"), creationMovie(6205)
	p.PosterPath = "/old.jpg"
	r := f.create(t, in, p, "first")
	execIntent(t, f.pool, "UPDATE media_artwork SET metadata_version=metadata_version+100,external_id='999',poster_path='/future.jpg' WHERE media_id=$1", r.Subscription.MediaID)
	q := db.New(f.pool)
	before, err := q.GetMediaByID(t.Context(), r.Subscription.MediaID)
	if err != nil {
		t.Fatal(err)
	}
	p.PosterPath = "/current.jpg"
	f.create(t, in, p, "current")
	requireCreationArtwork(t, f, r.Subscription.MediaID, "tmdb.movie", "6205", "/current.jpg")
	after, err := q.GetMediaByID(t.Context(), r.Subscription.MediaID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("display cache changed frozen media", err)
	}
}

func TestCreationArtworkTailFaultRollbackAndOriginalRetry(t *testing.T) {
	for _, table := range []string{"media_artwork", "audit_log", "outbox_event", "idempotency_record"} {
		t.Run(table, func(t *testing.T) {
			f := newCreationFixture(t, 117)
			in, p := creationInput(6206, "tv"), creationTV(6206, creationSeason(1, 1, 3))
			p.PosterPath = "/first.jpg"
			execIntent(t, f.pool, `CREATE FUNCTION artwork_tail_fault() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'private artwork fault';END$$`)
			for _, existing := range []bool{false, true} {
				key := "artwork-tail-new"
				if existing {
					key = "artwork-tail-existing"
					p = creationTV(6206, creationSeason(1, 1, 3, 5))
					p.PosterPath = "/updated.jpg"
				}
				id := creationIdentity(key)
				h := f.read(t, in, id)
				execIntent(t, f.pool, "CREATE TRIGGER artwork_fault BEFORE INSERT ON "+table+" FOR EACH ROW EXECUTE FUNCTION artwork_tail_fault()")
				before := creationArtworkSnapshot(t, f)
				_, err := f.store.CommitSubscriptionCreation(t.Context(), f.actor, in, h, p, id, creationDecider(in, h))
				if !errors.Is(err, ports.ErrUnavailable) || before != creationArtworkSnapshot(t, f) {
					t.Fatal("tail fault did not roll back artwork and business", err)
				}
				execIntent(t, f.pool, "DROP TRIGGER artwork_fault ON "+table)
				r, err := f.store.CommitSubscriptionCreation(t.Context(), f.actor, in, h, p, id, creationDecider(in, h))
				if err != nil {
					t.Fatal("original retry", err)
				}
				requireCreationArtwork(t, f, r.Subscription.MediaID, "tmdb.tv", "6206", p.PosterPath)
			}
		})
	}
}

func TestCreationArtworkPublicationFences(t *testing.T) {
	for _, mode := range []string{"callback", "permission", "stale-media", "external-id", "namespace", "tmdb-id"} {
		t.Run(mode, func(t *testing.T) {
			f := newCreationFixture(t, 92)
			in, p := creationInput(6207, "movie"), creationMovie(6207)
			if mode == "stale-media" {
				f.create(t, in, p, "seed")
			}
			p.PosterPath = "/blocked.jpg"
			id := creationIdentity("artwork-fence")
			h := f.read(t, in, id)
			want := ports.ErrSubscriptionBindingsConflict
			switch mode {
			case "permission":
				execIntent(t, f.pool, "UPDATE app_user SET is_active=false WHERE id=$1", uuid.MustParse(f.actor.ID))
				want = ports.ErrForbidden
			case "stale-media":
				execIntent(t, f.pool, "UPDATE media SET metadata_version=metadata_version+1 WHERE id=$1", h.MediaID)
			case "external-id":
				p.Media.ExternalID = "06207"
				want = ports.ErrInvalidSubscriptionBindings
			case "namespace":
				p.Media.Namespace = "tmdb.tv"
				want = ports.ErrInvalidSubscriptionBindings
			case "tmdb-id":
				p.TMDBID++
				want = ports.ErrInvalidSubscriptionBindings
			}
			before := creationArtworkSnapshot(t, f)
			_, err := f.store.CommitSubscriptionCreation(t.Context(), f.actor, in, h, p, id, func(c ports.SubscriptionCreateContext) (ports.SubscriptionCreateDecision, error) {
				d, e := creationDecider(in, h)(c)
				if mode == "callback" {
					d.Wanted[0].UnitID = uuid.New()
				}
				return d, e
			})
			if !errors.Is(err, want) || before != creationArtworkSnapshot(t, f) {
				t.Fatal("unauthorized artwork publication", err)
			}
		})
	}
}

func TestCreationArtworkCallbackCannotReplaceTrustedMaterial(t *testing.T) {
	f := newCreationFixture(t, 92)
	in, p := creationInput(6208, "movie"), creationMovie(6208)
	p.PosterPath = "/trusted.jpg"
	id := creationIdentity("artwork-frozen")
	h := f.read(t, in, id)
	r, err := f.store.CommitSubscriptionCreation(t.Context(), f.actor, in, h, p, id, func(c ports.SubscriptionCreateContext) (ports.SubscriptionCreateDecision, error) {
		d, e := creationDecider(in, h)(c)
		p.PosterPath = "/malicious.jpg"
		p.Media.ExternalID = "999"
		return d, e
	})
	if err != nil {
		t.Fatal(err)
	}
	requireCreationArtwork(t, f, r.Subscription.MediaID, "tmdb.movie", "6208", "/trusted.jpg")
}

func TestCreationArtworkConcurrentSameKey(t *testing.T) {
	f := newCreationFixture(t, 92)
	in, p := creationInput(6209, "movie"), creationMovie(6209)
	p.PosterPath = "/concurrent.jpg"
	id := creationIdentity("artwork-concurrent")
	h := f.read(t, in, id)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	var calls atomic.Int32
	type result struct {
		r *ports.SubscriptionCreationReceipt
		e error
	}
	results := make(chan result, 4)
	for range 4 {
		wg.Go(func() {
			r, err := f.store.CommitSubscriptionCreation(ctx, f.actor, in, h, p, id, func(c ports.SubscriptionCreateContext) (ports.SubscriptionCreateDecision, error) {
				calls.Add(1)
				return creationDecider(in, h)(c)
			})
			results <- result{r, err}
		})
	}
	wg.Wait()
	close(results)
	var first *ports.SubscriptionCreationReceipt
	for out := range results {
		if out.e != nil {
			t.Fatal(out.e)
		}
		if first == nil {
			first = out.r
		} else if !reflect.DeepEqual(first, out.r) {
			t.Fatal("same-key receipts diverged")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("repeated publication", calls.Load())
	}
	for _, table := range []string{"media", "media_artwork", "subscription", "idempotency_record", "audit_log", "outbox_event"} {
		if n := intentCount(t, f.pool, "SELECT count(*) FROM "+table); n != 1 {
			t.Fatal(table, n)
		}
	}
	requireCreationArtwork(t, f, first.Subscription.MediaID, "tmdb.movie", "6209", "/concurrent.jpg")
}

func TestCreationArtworkPreparedZeroPreservesHistoricalJSON(t *testing.T) {
	p := creationMovie(6210)
	// These are exactly the fields in the prepared material before PosterPath.
	historical := struct {
		TMDBID  int64
		Media   ports.SubscriptionMediaInput
		Units   []ports.SubscriptionMediaUnitSpec
		Seasons []ports.TVSeasonMetadata
	}{p.TMDBID, p.Media, p.Units, p.Seasons}
	old, err := json.Marshal(historical)
	if err != nil {
		t.Fatal(err)
	}
	current, err := json.Marshal(p)
	if err != nil || string(old) != string(current) {
		t.Fatalf("zero optional artwork changed historical JSON: %s / %s (%v)", old, current, err)
	}
	p.PosterPath = "/optional.jpg"
	current, err = json.Marshal(p)
	var fields map[string]json.RawMessage
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(current, &fields); err != nil || string(fields["poster_path"]) != `"/optional.jpg"` {
		t.Fatal("nonempty optional artwork missing", err)
	}
}
