package personfollow_test

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	domain "github.com/dalamudx/PulseMedia/internal/domain/personfollow"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/personfollow"
	"github.com/google/uuid"
)

type followRepository struct {
	saves, reads int
	result       *ports.PersonFollowReceipt
	rows         []ports.PersonFollowConfig
	err          error
	save         func(context.Context, *ports.Actor, uuid.UUID, ports.PersonFollowInput, string) (*ports.PersonFollowReceipt, error)
}

func (r *followRepository) SavePersonFollow(ctx context.Context, a *ports.Actor, id uuid.UUID, in ports.PersonFollowInput, key string) (*ports.PersonFollowReceipt, error) {
	r.saves++
	if r.save != nil {
		return r.save(ctx, a, id, in, key)
	}
	return r.result, r.err
}
func (r *followRepository) ReadPersonFollow(context.Context, *ports.Actor, uuid.UUID) (*ports.PersonFollowConfig, error) {
	r.reads++
	if r.result == nil {
		return nil, r.err
	}
	return &r.result.Config, r.err
}
func (r *followRepository) ListPersonFollows(context.Context, *ports.Actor) ([]ports.PersonFollowConfig, error) {
	r.reads++
	return r.rows, r.err
}
func (r *followRepository) ReadPersonFollowReceipt(context.Context, *ports.Actor, string) (*ports.PersonFollowReceipt, error) {
	r.reads++
	return r.result, r.err
}

func followInput() ports.PersonFollowInput {
	root := uuid.New()
	return ports.PersonFollowInput{Settings: domain.Settings{PersonID: 525, Role: "director", Enabled: true, MinReleaseDate: "2026-10-01", IntervalMinutes: 1440, MovieRootID: &root, QualityPolicyID: "default", QualityPolicyRevision: 1}}
}
func followActor() *ports.Actor {
	return &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember}
}
func followResult(a *ports.Actor, id uuid.UUID, in ports.PersonFollowInput, key string) *ports.PersonFollowReceipt {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	return &ports.PersonFollowReceipt{CommandKey: key, Config: ports.PersonFollowConfig{ID: id, OwnerID: uuid.MustParse(a.ID), Settings: in.Settings.Clone(), Version: in.ExpectedVersion + 1, CreatedAt: now, UpdatedAt: now}}
}

func TestPersonFollowConfigSaveAndIsolation(t *testing.T) {
	a := followActor()
	in := followInput()
	id := uuid.New()
	r := &followRepository{}
	r.save = func(_ context.Context, actor *ports.Actor, target uuid.UUID, input ports.PersonFollowInput, key string) (*ports.PersonFollowReceipt, error) {
		r.result = followResult(actor, target, input, key)
		return r.result, nil
	}
	service := personfollow.NewConfigService(r)
	got, err := service.Save(t.Context(), a, id, in, "original-key")
	if err != nil || got.Config.ID != id || got.Config.Version != 1 || r.saves != 1 {
		t.Fatal("configuration save failed", err)
	}
	original := *in.Settings.MovieRootID
	*got.Config.Settings.MovieRootID = uuid.New()
	if *in.Settings.MovieRootID != original || *r.result.Config.Settings.MovieRootID != original {
		t.Fatal("saved result shares pointer storage")
	}
}

func TestPersonFollowConfigAuthorityBeforeRepository(t *testing.T) {
	valid := followActor()
	scoped := *valid
	scoped.Scopes = []string{"read"}
	emptyScoped := *valid
	emptyScoped.Scopes = []string{}
	system := *valid
	system.Type = "system"
	badID := *valid
	badID.ID = "not-a-user"
	badRole := *valid
	badRole.Role = "guest"
	r := &followRepository{}
	service := personfollow.NewConfigService(r)
	for _, a := range []*ports.Actor{nil, &scoped, &emptyScoped, &system, &badID, &badRole} {
		want := ports.ErrForbidden
		if a == nil {
			want = ports.ErrUnauthorized
		}
		if _, err := service.Save(t.Context(), a, uuid.New(), followInput(), "key"); !errors.Is(err, want) {
			t.Fatal(err)
		}
		if _, err := service.Read(t.Context(), a, uuid.New()); !errors.Is(err, want) {
			t.Fatal(err)
		}
		if _, err := service.List(t.Context(), a); !errors.Is(err, want) {
			t.Fatal(err)
		}
		if _, err := service.Receipt(t.Context(), a, "key"); !errors.Is(err, want) {
			t.Fatal(err)
		}
	}
	if r.saves != 0 || r.reads != 0 {
		t.Fatal("untrusted actor reached storage")
	}
}

func TestPersonFollowConfigWriteBoundaries(t *testing.T) {
	a := followActor()
	r := &followRepository{}
	service := personfollow.NewConfigService(r)
	for _, version := range []int64{-1, math.MaxInt64} {
		in := followInput()
		in.ExpectedVersion = version
		if _, err := service.Save(t.Context(), a, uuid.New(), in, "key"); !errors.Is(err, ports.ErrRejected) {
			t.Fatal(err)
		}
	}
	for _, key := range []string{"", " key", "line\nbreak"} {
		if _, err := service.Save(t.Context(), a, uuid.New(), followInput(), key); !errors.Is(err, ports.ErrRejected) {
			t.Fatal(err)
		}
	}
	if _, err := service.Save(t.Context(), a, uuid.Nil, followInput(), "key"); !errors.Is(err, ports.ErrRejected) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := service.Save(ctx, a, uuid.New(), followInput(), "key"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if r.saves != 0 {
		t.Fatal("invalid command reached repository")
	}
}

func TestPersonFollowConfigRejectsUntrustedResults(t *testing.T) {
	a := followActor()
	in := followInput()
	id := uuid.New()
	cases := []func(*ports.PersonFollowReceipt){func(r *ports.PersonFollowReceipt) { r.CommandKey = "other" }, func(r *ports.PersonFollowReceipt) { r.Config.OwnerID = uuid.New() }, func(r *ports.PersonFollowReceipt) { r.Config.ID = uuid.New() }, func(r *ports.PersonFollowReceipt) { r.Config.Version = 2 }, func(r *ports.PersonFollowReceipt) { r.Config.Settings.Role = "actor" }, func(r *ports.PersonFollowReceipt) { r.Config.CreatedAt = time.Time{} }, func(r *ports.PersonFollowReceipt) { root := uuid.New(); r.Config.Settings.MovieRootID = &root }}
	for i, mutate := range cases {
		t.Run(string(rune('A'+i)), func(t *testing.T) {
			r := &followRepository{result: followResult(a, id, in, "key")}
			mutate(r.result)
			if got, err := personfollow.NewConfigService(r).Save(t.Context(), a, id, in, "key"); got != nil || !errors.Is(err, ports.ErrUnavailable) {
				t.Fatal("malformed receipt accepted", err)
			}
		})
	}
	r := &followRepository{err: errors.New("private DSN and SQL")}
	if _, err := personfollow.NewConfigService(r).Save(t.Context(), a, id, in, "key"); err != ports.ErrUnavailable {
		t.Fatal("internal error leaked", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r.save = func(_ context.Context, actor *ports.Actor, target uuid.UUID, input ports.PersonFollowInput, key string) (*ports.PersonFollowReceipt, error) {
		cancel()
		return followResult(actor, target, input, key), nil
	}
	if _, err := personfollow.NewConfigService(r).Save(ctx, a, id, in, "key"); !errors.Is(err, context.Canceled) {
		t.Fatal("late result accepted", err)
	}
}

func TestPersonFollowConfigReceiptAndListsAreReadOnly(t *testing.T) {
	a := followActor()
	id := uuid.New()
	r := &followRepository{result: followResult(a, id, followInput(), "key")}
	r.rows = []ports.PersonFollowConfig{r.result.Config}
	service := personfollow.NewConfigService(r)
	receipt, err := service.Receipt(t.Context(), a, "key")
	if err != nil {
		t.Fatal(err)
	}
	original := *r.result.Config.Settings.MovieRootID
	*receipt.Config.Settings.MovieRootID = uuid.New()
	if *r.result.Config.Settings.MovieRootID != original {
		t.Fatal("receipt shares storage")
	}
	if _, err := service.Read(t.Context(), a, id); err != nil {
		t.Fatal(err)
	}
	rows, err := service.List(t.Context(), a)
	if err != nil || len(rows) != 1 {
		t.Fatal(err)
	}
	*rows[0].Settings.MovieRootID = uuid.New()
	if *r.rows[0].Settings.MovieRootID != original {
		t.Fatal("list shares storage")
	}
	r.rows = append(r.rows, r.rows[0])
	if _, err := service.List(t.Context(), a); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal("duplicate configs accepted", err)
	}
	r.rows = nil
	rows, err = service.List(t.Context(), a)
	if err != nil || rows == nil || len(rows) != 0 {
		t.Fatal("empty list became unavailable", err)
	}
	if r.saves != 0 {
		t.Fatal("read path called Save")
	}
}

func TestPersonFollowConfigSnapshotsBeforeBlocking(t *testing.T) {
	a := followActor()
	owner := uuid.MustParse(a.ID)
	in := followInput()
	root := *in.Settings.MovieRootID
	id := uuid.New()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer func() {
		once.Do(func() { close(release) })
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("owned operation failed to finish")
		}
	}()
	r := &followRepository{}
	r.save = func(_ context.Context, actor *ports.Actor, target uuid.UUID, input ports.PersonFollowInput, key string) (*ports.PersonFollowReceipt, error) {
		close(entered)
		<-release
		return followResult(actor, target, input, key), nil
	}
	var result *ports.PersonFollowReceipt
	var saveErr error
	go func() {
		defer close(done)
		result, saveErr = personfollow.NewConfigService(r).Save(t.Context(), a, id, in, "key")
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("repository did not start")
	}
	a.ID = uuid.NewString()
	*in.Settings.MovieRootID = uuid.New()
	once.Do(func() { close(release) })
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("save did not finish")
	}
	if saveErr != nil || result.Config.OwnerID != owner || *result.Config.Settings.MovieRootID != root {
		t.Fatal("caller mutation changed in-flight intent", saveErr)
	}
}
