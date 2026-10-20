package mediascrape

import (
	"context"
	"errors"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type fakeRepo struct {
	rows          []ports.MediaScrapeCandidate
	reads, starts int
	err           error
	key           string
	actor         *ports.Actor
	reused        bool
}

func (f *fakeRepo) ListMediaScrapeCandidates(_ context.Context, a *ports.Actor) ([]ports.MediaScrapeCandidate, error) {
	f.reads++
	f.actor = a
	return f.rows, f.err
}
func (f *fakeRepo) StartMediaScrape(_ context.Context, a *ports.Actor, k string, allocate func() uuid.UUID) (ports.MediaScrapeProgress, bool, error) {
	f.starts++
	f.key = k
	f.actor = a
	return ports.MediaScrapeProgress{ID: allocate(), Total: 2}, f.reused, f.err
}
func (f *fakeRepo) ReadMediaScrapeBatch(_ context.Context, a *ports.Actor, k string) (*ports.MediaScrapeProgress, error) {
	f.reads++
	f.key = k
	f.actor = a
	return nil, f.err
}

type stubGen struct{}

func (stubGen) NewUUID() uuid.UUID    { return uuid.New() }
func (stubGen) NewUUIDString() string { return uuid.NewString() }
func admin() *ports.Actor {
	return &ports.Actor{Type: ports.ActorTypeUser, ID: "11111111-1111-1111-1111-111111111111", Role: ports.RoleAdmin}
}
func TestPreviewCountsRecordedStates(t *testing.T) {
	f := &fakeRepo{}
	for _, s := range []ports.MediaScrapeStatus{ports.MediaScrapeMissing, ports.MediaScrapeQueued, ports.MediaScrapeRunning, ports.MediaScrapeComplete, ports.MediaScrapeReview} {
		f.rows = append(f.rows, ports.MediaScrapeCandidate{Status: s})
	}
	p, err := NewService(f, stubGen{}).Preview(t.Context(), admin())
	if err != nil || p.Total != 5 || p.Missing != 1 || p.Queued != 1 || p.Running != 1 || p.Complete != 1 || p.Review != 1 {
		t.Fatalf("%+v %v", p, err)
	}
}
func TestAllOperationsRejectNonAdministratorWithoutRepositoryIO(t *testing.T) {
	actors := []*ports.Actor{nil, {Type: ports.ActorTypeUser, ID: admin().ID, Role: ports.RoleMember}, {Type: ports.ActorTypeUser, ID: admin().ID, Role: ports.RoleAdmin, Scopes: []string{}}, {Type: ports.ActorTypeService, ID: admin().ID, Role: ports.RoleAdmin}, {Type: ports.ActorTypeUser, ID: "bad", Role: ports.RoleAdmin}, {Type: ports.ActorTypeUser, ID: uuid.Nil.String(), Role: ports.RoleAdmin}}
	for _, a := range actors {
		f := &fakeRepo{}
		s := NewService(f, stubGen{})
		_, e := s.Preview(t.Context(), a)
		_, _, e2 := s.Start(t.Context(), a, "key")
		_, e3 := s.Status(t.Context(), a, "key")
		if e == nil || e2 == nil || e3 == nil || f.reads != 0 || f.starts != 0 {
			t.Fatalf("actor=%+v errors=%v/%v/%v", a, e, e2, e3)
		}
	}
}
func TestStartPassesOriginalKeyWithoutFreshCandidateRead(t *testing.T) {
	f := &fakeRepo{reused: true}
	a := admin()
	p, reused, err := NewService(f, stubGen{}).Start(t.Context(), a, "original-key")
	if err != nil || !reused || p.Total != 2 || f.key != "original-key" || f.actor != a || f.reads != 0 || f.starts != 1 {
		t.Fatalf("%+v %v reused=%t repo=%+v", p, err, reused, f)
	}
}
func TestInvalidKeysCauseNoRepositoryIO(t *testing.T) {
	f := &fakeRepo{}
	s := NewService(f, stubGen{})
	for _, k := range []string{"", "has space", "semi;colon", "新键"} {
		if _, _, e := s.Start(t.Context(), admin(), k); !errors.Is(e, ports.ErrRejected) {
			t.Fatal(e)
		}
	}
	if _, e := s.Status(t.Context(), admin(), "bad key"); !errors.Is(e, ports.ErrRejected) {
		t.Fatal(e)
	}
	if f.reads != 0 || f.starts != 0 {
		t.Fatal("invalid request reached repository")
	}
}
func TestUnavailableDependenciesAndReadFailures(t *testing.T) {
	f := &fakeRepo{err: ports.ErrUnavailable}
	s := NewService(f, stubGen{})
	if _, e := s.Preview(t.Context(), admin()); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal(e)
	}
	if _, e := s.Status(t.Context(), admin(), "key"); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal(e)
	}
	if _, _, e := s.Start(t.Context(), admin(), "key"); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal(e)
	}
	if _, _, e := NewService(f, nil).Start(t.Context(), admin(), "key"); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal(e)
	}
	if _, e := NewService(nil, stubGen{}).Preview(t.Context(), admin()); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal(e)
	}
}
func TestStatusUsesOriginalAccountKeyAndAllowsNoBatch(t *testing.T) {
	f := &fakeRepo{}
	a := admin()
	p, e := NewService(f, stubGen{}).Status(t.Context(), a, "original")
	if e != nil || p != nil || f.key != "original" || f.actor != a {
		t.Fatalf("%+v %v", p, e)
	}
}
