package personfollow_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	domain "github.com/dalamudx/PulseMedia/internal/domain/personfollow"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/personfollow"
	"github.com/google/uuid"
)

type personSyncPermit struct {
	work                                                     ports.PersonFollowSyncWork
	items                                                    []ports.PersonFollowSyncItem
	checks, failAt, stores, loads, reviews, finishes, closes int
	closed                                                   bool
}

func (p *personSyncPermit) Snapshot() ports.PersonFollowSyncWork { return p.work }
func (p *personSyncPermit) Check(context.Context) error {
	p.checks++
	if p.failAt > 0 && p.checks == p.failAt {
		return ports.ErrPersonFollowConflict
	}
	return nil
}
func (p *personSyncPermit) StoreCredits(_ context.Context, c ports.PersonCredits) error {
	p.stores++
	p.work.SourceStored = true
	p.items = make([]ports.PersonFollowSyncItem, 0, len(c.Works))
	for _, work := range c.Works {
		state, err := p.work.Config.Settings.Classify(work)
		if err != nil {
			return err
		}
		root := p.work.Config.Settings.RootFor(work.MediaType)
		p.items = append(p.items, ports.PersonFollowSyncItem{ID: uuid.New(), Observation: work, RootID: root, State: state, CommandKey: domain.CreationKey(p.work.Config.ID, p.work.Config.Version, root, work)})
	}
	return nil
}
func (p *personSyncPermit) Items(context.Context) ([]ports.PersonFollowSyncItem, error) {
	p.loads++
	return p.items, nil
}
func (p *personSyncPermit) ReviewItem(_ context.Context, id uuid.UUID) error {
	p.reviews++
	for i := range p.items {
		if p.items[i].ID == id {
			p.items[i].State = "review_required"
		}
	}
	return nil
}
func (p *personSyncPermit) FinishPage(context.Context) error { p.finishes++; return nil }
func (p *personSyncPermit) Close()                           { p.closes++; p.closed = true }

type personSyncRepo struct{ permit *personSyncPermit }

func (r personSyncRepo) EnqueueDue(context.Context) error     { return nil }
func (r personSyncRepo) RecoverExpired(context.Context) error { return nil }
func (r personSyncRepo) Acquire(context.Context, ports.JobLease) (ports.PersonFollowSyncPermit, error) {
	r.permit.closed = false
	return r.permit, nil
}

type personCreditsFunc func(context.Context, int64, string) (ports.PersonCredits, error)

func (f personCreditsFunc) GetPersonCredits(ctx context.Context, id int64, role string) (ports.PersonCredits, error) {
	return f(ctx, id, role)
}

type personCreatorFunc func(context.Context, ports.PersonFollowCreationFence, *ports.Actor, ports.SubscriptionCreationInput, string) (*ports.SubscriptionCreationReceipt, error)

func (f personCreatorFunc) CreatePersonFollowSubscription(ctx context.Context, fence ports.PersonFollowCreationFence, a *ports.Actor, in ports.SubscriptionCreationInput, key string) (*ports.SubscriptionCreationReceipt, error) {
	return f(ctx, fence, a, in, key)
}
func newPersonSyncPermit() (*personSyncPermit, ports.JobLease) {
	owner, root := uuid.New(), uuid.New()
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	config := ports.PersonFollowConfig{ID: uuid.New(), OwnerID: owner, Version: 1, CreatedAt: now, UpdatedAt: now, Settings: domain.Settings{PersonID: 525, Role: "director", Enabled: true, MinReleaseDate: "2026-10-01", IntervalMinutes: 1440, MovieRootID: &root, QualityPolicyID: "default", QualityPolicyRevision: 1}}
	p := &personSyncPermit{work: ports.PersonFollowSyncWork{RunID: uuid.New(), ExecutionID: uuid.New(), Config: config, Actor: ports.Actor{Type: ports.ActorTypeUser, ID: owner.String(), Role: ports.RoleMember}}}
	return p, ports.JobLease{ID: uuid.New(), Owner: "owned-worker", Token: 1}
}
func personSyncCredits() ports.PersonCredits {
	return ports.PersonCredits{PersonID: 525, Role: "director", Works: []ports.PersonWork{{TMDBID: 550, MediaType: "movie", Title: "A real work", ReleaseDate: "2026-10-01"}}}
}
func personSyncReceipt(p *personSyncPermit, in ports.SubscriptionCreationInput) *ports.SubscriptionCreationReceipt {
	return &ports.SubscriptionCreationReceipt{Subscription: ports.SubscriptionRecord{ID: uuid.New(), UserID: p.work.Config.OwnerID, RootID: *in.RootID, MediaID: uuid.New()}}
}
func TestPersonFollowSyncSourceAndEligibility(t *testing.T) {
	p, lease := newPersonSyncPermit()
	sourceCalls, created := 0, 0
	credits := personSyncCredits()
	credits.Works = append(credits.Works, ports.PersonWork{TMDBID: 551, MediaType: "movie", Title: "Older work", ReleaseDate: "2026-09-30"}, ports.PersonWork{TMDBID: 552, MediaType: "movie", Title: "Unknown date"}, ports.PersonWork{TMDBID: 550, MediaType: "tv", Title: "Unselected series", ReleaseDate: "2026-10-01"})
	provider := personCreditsFunc(func(_ context.Context, id int64, role string) (ports.PersonCredits, error) {
		sourceCalls++
		if id != 525 || role != "director" {
			t.Fatal("person identity changed")
		}
		return credits, nil
	})
	creator := personCreatorFunc(func(_ context.Context, f ports.PersonFollowCreationFence, a *ports.Actor, in ports.SubscriptionCreationInput, key string) (*ports.SubscriptionCreationReceipt, error) {
		created++
		if f.Lease != lease || f.ConfigID != p.work.Config.ID || f.ConfigVersion != 1 || f.RunID != p.work.RunID || f.ExecutionID != p.work.ExecutionID || a.ID != p.work.Actor.ID || *in.TMDBID != 550 || in.MediaType != "movie" || *in.RootID != *p.work.Config.Settings.MovieRootID || in.ScopeType != "all" || key == "" {
			t.Fatal("creation lost frozen scope or lease")
		}
		return personSyncReceipt(p, in), nil
	})
	if err := personfollow.NewSyncService(personSyncRepo{p}, provider, creator).Execute(t.Context(), lease); err != nil {
		t.Fatal(err)
	}
	if sourceCalls != 1 || created != 1 || p.stores != 1 || p.finishes != 1 || p.closes != 1 {
		t.Fatal("unexpected execution", sourceCalls, created, p)
	}
}
func TestPersonFollowSyncRejectsSourceAndLatePermission(t *testing.T) {
	for _, change := range []func(*ports.PersonCredits){func(c *ports.PersonCredits) { c.PersonID++ }, func(c *ports.PersonCredits) { c.Role = "actor" }, func(c *ports.PersonCredits) { c.Works = nil }, func(c *ports.PersonCredits) { c.Works[0].ReleaseDate = "2026-02-30" }, func(c *ports.PersonCredits) {
		other := c.Works[0]
		other.Title = "Conflict"
		c.Works = append(c.Works, other)
	}} {
		p, lease := newPersonSyncPermit()
		credits := personSyncCredits()
		change(&credits)
		creatorCalls := 0
		s := personfollow.NewSyncService(personSyncRepo{p}, personCreditsFunc(func(context.Context, int64, string) (ports.PersonCredits, error) { return credits, nil }), personCreatorFunc(func(context.Context, ports.PersonFollowCreationFence, *ports.Actor, ports.SubscriptionCreationInput, string) (*ports.SubscriptionCreationReceipt, error) {
			creatorCalls++
			return nil, nil
		}))
		if err := s.Execute(t.Context(), lease); !errors.Is(err, ports.ErrBadResponse) || creatorCalls != 0 || p.stores != 0 || p.closes != 1 {
			t.Fatal("unproven source escaped", err)
		}
	}
	for _, at := range []int{1, 2, 3} {
		p, lease := newPersonSyncPermit()
		p.failAt = at
		calls := 0
		s := personfollow.NewSyncService(personSyncRepo{p}, personCreditsFunc(func(context.Context, int64, string) (ports.PersonCredits, error) { return personSyncCredits(), nil }), personCreatorFunc(func(context.Context, ports.PersonFollowCreationFence, *ports.Actor, ports.SubscriptionCreationInput, string) (*ports.SubscriptionCreationReceipt, error) {
			calls++
			return nil, nil
		}))
		if err := s.Execute(t.Context(), lease); !errors.Is(err, ports.ErrPersonFollowConflict) || calls != 0 || p.finishes != 0 || p.closes != 1 {
			t.Fatal("late authority escaped", err)
		}
	}
}
func TestPersonFollowSyncResumeDoesNotRepeat(t *testing.T) {
	p, lease := newPersonSyncPermit()
	reads, creates := 0, 0
	provider := personCreditsFunc(func(context.Context, int64, string) (ports.PersonCredits, error) {
		reads++
		return personSyncCredits(), nil
	})
	creator := personCreatorFunc(func(_ context.Context, f ports.PersonFollowCreationFence, _ *ports.Actor, _ ports.SubscriptionCreationInput, _ string) (*ports.SubscriptionCreationReceipt, error) {
		creates++
		for i := range p.items {
			if p.items[i].ID == f.ItemID {
				p.items[i].State = "created"
			}
		}
		return nil, ports.ErrUnavailable
	})
	service := personfollow.NewSyncService(personSyncRepo{p}, provider, creator)
	if err := service.Execute(t.Context(), lease); !errors.Is(err, ports.ErrUnavailable) || p.finishes != 0 {
		t.Fatal("lost commit receipt became success", err)
	}
	lease.Token++
	p.work.ExecutionID = uuid.New()
	if err := service.Execute(t.Context(), lease); err != nil {
		t.Fatal(err)
	}
	if reads != 1 || creates != 1 || p.finishes != 1 || p.closes != 2 {
		t.Fatal("recovery reread source or repeated creation", reads, creates, p)
	}
}
func TestPersonFollowSyncBadPageMakesNoCreation(t *testing.T) {
	for _, change := range []func(*personSyncPermit){func(p *personSyncPermit) { p.items = nil }, func(p *personSyncPermit) { p.items[0].RootID = uuid.New() }, func(p *personSyncPermit) { p.items[0].CommandKey = "foreign-command" }, func(p *personSyncPermit) { p.items = append(p.items, p.items[0]) }, func(p *personSyncPermit) { p.items[0].Observation.ReleaseDate = "" }, func(p *personSyncPermit) { p.items[0].State = "fabricated" }, func(p *personSyncPermit) { p.items = slices.Repeat(p.items, 65) }} {
		p, lease := newPersonSyncPermit()
		if err := p.StoreCredits(t.Context(), personSyncCredits()); err != nil {
			t.Fatal(err)
		}
		change(p)
		creates := 0
		s := personfollow.NewSyncService(personSyncRepo{p}, personCreditsFunc(func(context.Context, int64, string) (ports.PersonCredits, error) {
			t.Fatal("sealed observation reread")
			return ports.PersonCredits{}, nil
		}), personCreatorFunc(func(context.Context, ports.PersonFollowCreationFence, *ports.Actor, ports.SubscriptionCreationInput, string) (*ports.SubscriptionCreationReceipt, error) {
			creates++
			return nil, nil
		}))
		if err := s.Execute(t.Context(), lease); !errors.Is(err, ports.ErrBadResponse) || creates != 0 || p.finishes != 0 || p.closes != 1 {
			t.Fatal("bad page partially executed", err)
		}
	}
}
func TestPersonFollowSyncConflictsAndReceipts(t *testing.T) {
	p, lease := newPersonSyncPermit()
	credits := personSyncCredits()
	other := credits.Works[0]
	other.TMDBID++
	credits.Works = append(credits.Works, other)
	creates := 0
	creator := personCreatorFunc(func(_ context.Context, _ ports.PersonFollowCreationFence, _ *ports.Actor, in ports.SubscriptionCreationInput, _ string) (*ports.SubscriptionCreationReceipt, error) {
		creates++
		if *in.TMDBID == 550 {
			return nil, ports.ErrSubscriptionBindingsConflict
		}
		return personSyncReceipt(p, in), nil
	})
	if err := personfollow.NewSyncService(personSyncRepo{p}, personCreditsFunc(func(context.Context, int64, string) (ports.PersonCredits, error) { return credits, nil }), creator).Execute(t.Context(), lease); err != nil || creates != 2 || p.reviews != 1 || p.finishes != 1 {
		t.Fatal("one conflict blocked independent work", err)
	}
	p, lease = newPersonSyncPermit()
	wrong := personCreatorFunc(func(_ context.Context, _ ports.PersonFollowCreationFence, _ *ports.Actor, in ports.SubscriptionCreationInput, _ string) (*ports.SubscriptionCreationReceipt, error) {
		r := personSyncReceipt(p, in)
		r.Subscription.UserID = uuid.New()
		return r, nil
	})
	if err := personfollow.NewSyncService(personSyncRepo{p}, personCreditsFunc(func(context.Context, int64, string) (ports.PersonCredits, error) { return personSyncCredits(), nil }), wrong).Execute(t.Context(), lease); !errors.Is(err, ports.ErrBadResponse) || p.finishes != 0 {
		t.Fatal("foreign receipt accepted", err)
	}
}
func TestPersonFollowSyncCancellationWaitsForPhysicalCall(t *testing.T) {
	p, lease := newPersonSyncPermit()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	var once sync.Once
	defer func() { once.Do(func() { close(release) }); <-finished }()
	provider := personCreditsFunc(func(context.Context, int64, string) (ports.PersonCredits, error) {
		close(entered)
		<-release
		return personSyncCredits(), nil
	})
	service := personfollow.NewSyncService(personSyncRepo{p}, provider, personCreatorFunc(func(context.Context, ports.PersonFollowCreationFence, *ports.Actor, ports.SubscriptionCreationInput, string) (*ports.SubscriptionCreationReceipt, error) {
		t.Error("cancelled call created a subscription")
		return nil, nil
	}))
	go func() { defer close(finished); done <- service.Execute(ctx, lease) }()
	<-entered
	cancel()
	select {
	case <-done:
		t.Fatal("released while physical call remained blocked")
	default:
	}
	if p.closed {
		t.Fatal("permit closed before physical return")
	}
	once.Do(func() { close(release) })
	if err := <-done; !errors.Is(err, context.Canceled) || p.stores != 0 || p.finishes != 0 || !p.closed {
		t.Fatal("late cancelled result escaped", err)
	}
}
func TestPersonFollowSyncSafeErrorsAndInvalidWork(t *testing.T) {
	for _, sourceErr := range []error{errors.New("owned-private URL"), ports.NewRateLimitError(2*time.Minute, "owned-private endpoint"), ports.ErrAuthRequired} {
		p, lease := newPersonSyncPermit()
		s := personfollow.NewSyncService(personSyncRepo{p}, personCreditsFunc(func(context.Context, int64, string) (ports.PersonCredits, error) {
			return ports.PersonCredits{}, sourceErr
		}), personCreatorFunc(func(context.Context, ports.PersonFollowCreationFence, *ports.Actor, ports.SubscriptionCreationInput, string) (*ports.SubscriptionCreationReceipt, error) {
			t.Fatal("failed source created work")
			return nil, nil
		}))
		err := s.Execute(t.Context(), lease)
		if err == nil || strings.Contains(err.Error(), "owned-private") || p.stores != 0 || p.closes != 1 {
			t.Fatal("unsafe source failure", err)
		}
		if rate, ok := ports.IsRateLimited(sourceErr); ok {
			got, ok := ports.IsRateLimited(err)
			if !ok || got.RetryAfter != rate.RetryAfter {
				t.Fatal("retry advice lost")
			}
		}
	}
	p, lease := newPersonSyncPermit()
	p.work.Config.OwnerID = uuid.New()
	reads := 0
	s := personfollow.NewSyncService(personSyncRepo{p}, personCreditsFunc(func(context.Context, int64, string) (ports.PersonCredits, error) {
		reads++
		return personSyncCredits(), nil
	}), personCreatorFunc(func(context.Context, ports.PersonFollowCreationFence, *ports.Actor, ports.SubscriptionCreationInput, string) (*ports.SubscriptionCreationReceipt, error) {
		return nil, nil
	}))
	if err := s.Execute(t.Context(), lease); !errors.Is(err, ports.ErrRejected) || reads != 0 || p.closes != 1 {
		t.Fatal("forged owner reached provider", err)
	}
}
