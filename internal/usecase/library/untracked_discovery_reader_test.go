package library

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type discoveryReadFake func(context.Context, *ports.Actor, uuid.UUID, string, int) (ports.UntrackedDiscoveryPage, error)

func (f discoveryReadFake) ReadUntrackedDiscovery(ctx context.Context, a *ports.Actor, id uuid.UUID, c string, n int) (ports.UntrackedDiscoveryPage, error) {
	return f(ctx, a, id, c, n)
}
func discoveryReadFixture(t *testing.T) (*ports.Actor, uuid.UUID, ports.UntrackedDiscoveredFile) {
	t.Helper()
	_, r, _, _ := discoveryTestFixture(t)
	scope := r.capture.snapshot.Scope
	stamp := time.Now().UTC()
	return &ports.Actor{Type: ports.ActorTypeUser, ID: scope.OwnerID.String(), Role: ports.RoleAdmin}, scope.ID, ports.UntrackedDiscoveredFile{ID: uuid.New(), ScopeID: scope.ID, ScopeVersion: scope.Version, Revision: 1, RelativePath: "Movie.mkv", Identity: ports.FileIdentity{Device: 1, Inode: 2, Size: 10, Mtime: 3}, FirstObservedAt: stamp, LastObservedAt: stamp, Observations: 1}
}
func TestUntrackedDiscoveryReadIsolation(t *testing.T) {
	actor, id, file := discoveryReadFixture(t)
	original := actor.ID
	items := []ports.UntrackedDiscoveredFile{file}
	calls := 0
	svc := NewUntrackedDiscoveryReader(discoveryReadFake(func(_ context.Context, a *ports.Actor, scope uuid.UUID, c string, n int) (ports.UntrackedDiscoveryPage, error) {
		calls++
		if scope != id || c != "" || n != 64 {
			t.Fatal("read arguments changed")
		}
		a.ID = uuid.NewString()
		return ports.UntrackedDiscoveryPage{Files: items, Complete: true}, nil
	}))
	out, err := svc.Read(t.Context(), actor, id, "", 0)
	if err != nil || calls != 1 || actor.ID != original || len(out.Files) != 1 {
		t.Fatal(out, err)
	}
	out.Files[0].RelativePath = "mutated"
	if items[0].RelativePath != "Movie.mkv" {
		t.Fatal("returned slice aliases repository")
	}
}
func TestUntrackedDiscoveryReadRejectsSubjectsAndInput(t *testing.T) {
	actor, id, _ := discoveryReadFixture(t)
	for _, kind := range []string{"anonymous", "member", "service", "token", "scope", "limit-negative", "limit-large", "cursor-newline", "cursor-large", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			svc := NewUntrackedDiscoveryReader(discoveryReadFake(func(context.Context, *ports.Actor, uuid.UUID, string, int) (ports.UntrackedDiscoveryPage, error) {
				calls++
				return ports.UntrackedDiscoveryPage{}, nil
			}))
			a := *actor
			who, scope, cursor, n, ctx := &a, id, "", 64, t.Context()
			switch kind {
			case "anonymous":
				who = nil
			case "member":
				a.Role = ports.RoleMember
			case "service":
				a.Type = ports.ActorTypeService
			case "token":
				a.Scopes = []string{}
			case "scope":
				scope = uuid.Nil
			case "limit-negative":
				n = -1
			case "limit-large":
				n = 65
			case "cursor-newline":
				cursor = "unsafe\n"
			case "cursor-large":
				cursor = strings.Repeat("x", 2049)
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if _, err := svc.Read(ctx, who, scope, cursor, n); err == nil || calls != 0 {
				t.Fatal("invalid read entered repository", err)
			}
		})
	}
}
func TestUntrackedDiscoveryReadRejectsMalformedRows(t *testing.T) {
	actor, id, file := discoveryReadFixture(t)
	for _, kind := range []string{"foreign", "path", "nonvideo", "identity", "revision", "date", "count", "duplicate", "cursor"} {
		t.Run(kind, func(t *testing.T) {
			f := file
			out := ports.UntrackedDiscoveryPage{Complete: true}
			switch kind {
			case "foreign":
				f.ScopeID = uuid.New()
			case "path":
				f.RelativePath = "../Movie.mkv"
			case "nonvideo":
				f.RelativePath = "Movie.nfo"
			case "identity":
				f.Identity.Inode = 0
			case "revision":
				f.Revision = 0
			case "date":
				f.FirstObservedAt = time.Time{}
			case "count":
				f.Observations = 0
			case "cursor":
				out.NextCursor = "inconsistent"
			}
			out.Files = []ports.UntrackedDiscoveredFile{f}
			if kind == "duplicate" {
				out.Files = append(out.Files, f)
			}
			svc := NewUntrackedDiscoveryReader(discoveryReadFake(func(context.Context, *ports.Actor, uuid.UUID, string, int) (ports.UntrackedDiscoveryPage, error) {
				return out, nil
			}))
			if _, err := svc.Read(t.Context(), actor, id, "", 64); !errors.Is(err, ports.ErrUnavailable) {
				t.Fatal(err)
			}
		})
	}
}
func TestUntrackedDiscoveryReadSafeErrors(t *testing.T) {
	actor, id, _ := discoveryReadFixture(t)
	svc := NewUntrackedDiscoveryReader(discoveryReadFake(func(context.Context, *ports.Actor, uuid.UUID, string, int) (ports.UntrackedDiscoveryPage, error) {
		return ports.UntrackedDiscoveryPage{}, errors.New("secret path /source/private")
	}))
	if _, err := svc.Read(t.Context(), actor, id, "", 64); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal(err)
	}
	var absent *UntrackedDiscoveryReader
	if _, err := absent.Read(t.Context(), actor, id, "", 64); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal(err)
	}
}
