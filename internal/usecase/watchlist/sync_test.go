package watchlist_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/watchlist"
	"github.com/google/uuid"
)

type syncRepo struct {
	p        *syncPermit
	err      error
	acquires int
}

func (r *syncRepo) EnqueueDue(context.Context) error     { return nil }
func (r *syncRepo) RecoverExpired(context.Context) error { return nil }
func (r *syncRepo) Acquire(context.Context, ports.JobLease) (ports.DoubanWishSyncPermit, error) {
	r.acquires++
	return r.p, r.err
}

type wishProvider func(context.Context, string, string, int) (ports.DoubanWishPage, error)

func (f wishProvider) ReadWishPage(ctx context.Context, u, k string, start int) (ports.DoubanWishPage, error) {
	return f(ctx, u, k, start)
}

type wishCreator func(context.Context, ports.DoubanWishCreationFence, *ports.Actor, ports.SubscriptionCreationInput, string) (*ports.SubscriptionCreationReceipt, error)

func (f wishCreator) CreateDoubanWishSubscription(ctx context.Context, x ports.DoubanWishCreationFence, a *ports.Actor, in ports.SubscriptionCreationInput, key string) (*ports.SubscriptionCreationReceipt, error) {
	return f(ctx, x, a, in, key)
}

type syncPermit struct {
	work                                       ports.DoubanWishSyncWork
	items                                      []ports.DoubanWishSyncItem
	checks, pages, matches, finished, failures int
	closed                                     atomic.Int32
	checkAt                                    int
	lastMatch                                  ports.DoubanWishMatch
	lastFailure                                string
	pageHook                                   func(ports.DoubanWishPage)
}

func (p *syncPermit) Snapshot() ports.DoubanWishSyncWork { return p.work }
func (p *syncPermit) Check(context.Context) error {
	p.checks++
	if p.checkAt > 0 && p.checks >= p.checkAt {
		return ports.ErrRejected
	}
	return nil
}
func (p *syncPermit) StorePage(_ context.Context, page ports.DoubanWishPage) ([]ports.DoubanWishSyncItem, error) {
	p.pages++
	if p.pageHook != nil {
		p.pageHook(page)
	}
	return p.items, nil
}
func (p *syncPermit) StoreIdentity(_ context.Context, _ uuid.UUID, m ports.DoubanWishMatch) error {
	p.matches++
	p.lastMatch = m
	return nil
}
func (p *syncPermit) FinishItem(_ context.Context, _ uuid.UUID, state string) error {
	p.failures++
	p.lastFailure = state
	return nil
}
func (p *syncPermit) FinishPage(context.Context) error { p.finished++; return nil }
func (p *syncPermit) Close()                           { p.closed.Add(1) }
func syncFixture(kind string) (*syncRepo, wishProvider, *metadata, wishCreator, ports.JobLease) {
	a, _, c := configFixture()
	cfg := *c.current
	if kind == "tv" {
		cfg.TVRootID = cfg.MovieRootID
		cfg.MovieRootID = nil
	}
	p := &syncPermit{work: ports.DoubanWishSyncWork{RunID: uuid.New(), Config: cfg, Actor: *a, MediaType: kind, ExecutionID: uuid.New()}, items: []ports.DoubanWishSyncItem{{ID: uuid.New(), Observation: item(kind), State: "pending", CommandKey: "douban-wish:original-key"}}}
	provider := wishProvider(func(_ context.Context, u, k string, start int) (ports.DoubanWishPage, error) {
		if u != "exherb" || k != kind || start != 0 {
			panic("wrong source scope")
		}
		return ports.DoubanWishPage{RequestedID: u, CanonicalID: u, MediaType: k, WindowEnd: 1, WindowComplete: true, Total: 1, Items: []ports.DoubanWishItem{item(k)}}, nil
	})
	m := meta()
	m.search = fixed(candidate(7, kind, "测试作品", 2020))
	create := wishCreator(func(_ context.Context, x ports.DoubanWishCreationFence, actor *ports.Actor, in ports.SubscriptionCreationInput, key string) (*ports.SubscriptionCreationReceipt, error) {
		root := cfg.MovieRootID
		if kind == "tv" {
			root = cfg.TVRootID
		}
		if x.ItemID != p.items[0].ID || x.RunID != p.work.RunID || x.ConfigID != cfg.ID || x.ConfigVersion != cfg.Version || x.ExecutionID != p.work.ExecutionID || actor.ID != a.ID || key != p.items[0].CommandKey || in.TMDBID == nil || *in.TMDBID != 7 || in.MediaType != kind || in.RootID == nil || *in.RootID != *root || in.ExpectedIntentVersion != nil || in.IndexerIDs != nil || in.DownloaderID != nil || in.ScopeType != "all" || in.QualityPolicyID != "default" {
			panic("wrong creation authority or intent")
		}
		return &ports.SubscriptionCreationReceipt{Subscription: ports.SubscriptionRecord{ID: uuid.New(), UserID: cfg.OwnerID, RootID: *root}}, nil
	})
	return &syncRepo{p: p}, provider, m, create, ports.JobLease{ID: uuid.New(), Owner: "owned-worker", Token: 3}
}
func TestWishSyncMovieAndTV(t *testing.T) {
	for _, kind := range []string{"movie", "tv"} {
		t.Run(kind, func(t *testing.T) {
			r, p, m, c, lease := syncFixture(kind)
			if err := watchlist.NewSyncService(r, p, m, c).Execute(t.Context(), lease); err != nil {
				t.Fatal(err)
			}
			if r.p.pages != 1 || r.p.matches != 1 || r.p.lastMatch.State != "matched" || r.p.finished != 1 || r.p.failures != 0 || r.p.closed.Load() != 1 {
				t.Fatalf("unexpected pipeline state %+v", r.p)
			}
		})
	}
}
func TestWishSyncWithholdsAndPreservesItems(t *testing.T) {
	for _, state := range []string{"unresolved", "incomplete", "ambiguous", "created", "conflict", "unavailable"} {
		t.Run("existing_"+state, func(t *testing.T) {
			r, p, m, _, lease := syncFixture("movie")
			r.p.items[0].State = state
			c := wishCreator(func(context.Context, ports.DoubanWishCreationFence, *ports.Actor, ports.SubscriptionCreationInput, string) (*ports.SubscriptionCreationReceipt, error) {
				t.Fatal("replayed terminal item")
				return nil, nil
			})
			if err := watchlist.NewSyncService(r, p, m, c).Execute(t.Context(), lease); err != nil || r.p.finished != 1 || r.p.matches != 0 || len(m.queries) != 0 {
				t.Fatalf("%v %+v", err, r.p)
			}
		})
	}
	t.Run("unknown_year_no_metadata", func(t *testing.T) {
		r, p, m, _, lease := syncFixture("movie")
		r.p.items[0].Observation.Year = 0
		c := wishCreator(func(context.Context, ports.DoubanWishCreationFence, *ports.Actor, ports.SubscriptionCreationInput, string) (*ports.SubscriptionCreationReceipt, error) {
			t.Fatal("created unknown identity")
			return nil, nil
		})
		if err := watchlist.NewSyncService(r, p, m, c).Execute(t.Context(), lease); err != nil || r.p.lastMatch.State != "unresolved" || r.p.finished != 1 || len(m.queries) != 0 {
			t.Fatalf("%v %+v", err, r.p)
		}
	})
	t.Run("existing_intent_conflict", func(t *testing.T) {
		r, p, m, _, lease := syncFixture("movie")
		c := wishCreator(func(context.Context, ports.DoubanWishCreationFence, *ports.Actor, ports.SubscriptionCreationInput, string) (*ports.SubscriptionCreationReceipt, error) {
			return nil, ports.ErrSubscriptionBindingsConflict
		})
		if err := watchlist.NewSyncService(r, p, m, c).Execute(t.Context(), lease); err != nil || r.p.lastFailure != "conflict" || r.p.finished != 1 {
			t.Fatalf("%v %+v", err, r.p)
		}
	})
}
func TestWishSyncCancellationAndLeaseFences(t *testing.T) {
	for _, stage := range []string{"before_acquire", "after_page", "after_metadata", "after_create"} {
		t.Run(stage, func(t *testing.T) {
			r, p, m, c, lease := syncFixture("movie")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch stage {
			case "before_acquire":
				cancel()
			case "after_page":
				old := p
				p = func(ctx context.Context, u, k string, n int) (ports.DoubanWishPage, error) {
					out, err := old(ctx, u, k, n)
					cancel()
					return out, err
				}
			case "after_metadata":
				m.cancelDetail = cancel
			case "after_create":
				old := c
				c = func(ctx context.Context, x ports.DoubanWishCreationFence, a *ports.Actor, in ports.SubscriptionCreationInput, key string) (*ports.SubscriptionCreationReceipt, error) {
					out, err := old(ctx, x, a, in, key)
					cancel()
					return out, err
				}
			}
			err := watchlist.NewSyncService(r, p, m, c).Execute(ctx, lease)
			if !errors.Is(err, context.Canceled) || r.p.finished != 0 {
				t.Fatalf("%v %+v", err, r.p)
			}
			if stage == "before_acquire" {
				if r.acquires != 0 {
					t.Fatal("acquired after cancellation")
				}
			} else if r.p.closed.Load() != 1 {
				t.Fatal("physical permit leaked")
			}
		})
	}
	for _, checkAt := range []int{1, 2, 3, 4, 5, 6, 7} {
		t.Run("lease_check_"+string(rune('0'+checkAt)), func(t *testing.T) {
			r, p, m, c, lease := syncFixture("movie")
			r.p.checkAt = checkAt
			err := watchlist.NewSyncService(r, p, m, c).Execute(t.Context(), lease)
			if !errors.Is(err, ports.ErrRejected) || r.p.finished != 0 || r.p.closed.Load() != 1 {
				t.Fatalf("%v %+v", err, r.p)
			}
		})
	}
}
func TestWishSyncKeepsPhysicalPermitUntilIgnoredCancellationReturns(t *testing.T) {
	r, _, m, c, lease := syncFixture("movie")
	started, release := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	p := wishProvider(func(context.Context, string, string, int) (ports.DoubanWishPage, error) {
		close(started)
		<-release
		return ports.DoubanWishPage{}, nil
	})
	done := make(chan error, 1)
	go func() { done <- watchlist.NewSyncService(r, p, m, c).Execute(ctx, lease) }()
	<-started
	cancel()
	if r.p.closed.Load() != 0 {
		t.Fatal("closed permit before physical return")
	}
	select {
	case <-done:
		t.Fatal("detached still-running provider")
	default:
	}
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) || r.p.closed.Load() != 1 || r.p.pages != 0 {
		t.Fatalf("%v closed=%d pages=%d", err, r.p.closed.Load(), r.p.pages)
	}
}
func TestWishSyncRejectsMalformedBindings(t *testing.T) {
	for _, stage := range []string{"work_owner", "work_root", "work_execution", "work_start", "item_id", "item_kind", "item_key", "item_state", "duplicate"} {
		t.Run(stage, func(t *testing.T) {
			r, p, m, c, lease := syncFixture("movie")
			switch stage {
			case "work_owner":
				r.p.work.Actor.ID = uuid.NewString()
			case "work_root":
				r.p.work.Config.MovieRootID = nil
			case "work_execution":
				r.p.work.ExecutionID = uuid.Nil
			case "work_start":
				r.p.work.Start = ports.MaxDoubanWishStart
			case "item_id":
				r.p.items[0].ID = uuid.Nil
			case "item_kind":
				r.p.items[0].Observation.MediaType = "tv"
			case "item_key":
				r.p.items[0].CommandKey = "invalid key"
			case "item_state":
				r.p.items[0].State = "failed"
			case "duplicate":
				r.p.items = append(r.p.items, r.p.items[0])
			}
			err := watchlist.NewSyncService(r, p, m, c).Execute(t.Context(), lease)
			if !errors.Is(err, ports.ErrRejected) || r.p.finished != 0 || r.p.closed.Load() != 1 {
				t.Fatalf("%v %+v", err, r.p)
			}
		})
	}
}
func TestWishSyncRejectsLateIdentityAndUnsafeErrors(t *testing.T) {
	r, p, m, c, lease := syncFixture("movie")
	m.err = errors.New("credential URL must never appear")
	err := watchlist.NewSyncService(r, p, m, c).Execute(t.Context(), lease)
	if !errors.Is(err, ports.ErrUnavailable) || r.p.finished != 0 || r.p.lastFailure != "unavailable" || r.p.closed.Load() != 1 {
		t.Fatalf("%v %+v", err, r.p)
	}
	r, p, m, _, lease = syncFixture("movie")
	c = func(context.Context, ports.DoubanWishCreationFence, *ports.Actor, ports.SubscriptionCreationInput, string) (*ports.SubscriptionCreationReceipt, error) {
		return &ports.SubscriptionCreationReceipt{}, nil
	}
	err = watchlist.NewSyncService(r, p, m, c).Execute(t.Context(), lease)
	if !errors.Is(err, ports.ErrUnavailable) || r.p.finished != 0 {
		t.Fatal(err)
	}
}
