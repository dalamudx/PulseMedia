package library

import (
	"context"
	"errors"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type reviewScopeRepo struct {
	*untrackedReviewTestRepo
	page   ports.UntrackedReviewScopePage
	calls  int
	cursor string
}

func (r *reviewScopeRepo) ReadUntrackedReviewScopes(_ context.Context, _ *ports.Actor, cursor string) (ports.UntrackedReviewScopePage, error) {
	r.calls++
	r.cursor = cursor
	return r.page, r.err
}

func TestUntrackedReviewScopeOptionsService(t *testing.T) {
	f := untrackedReviewFixture()
	item := ports.UntrackedReviewScopeOption{ID: uuid.NewString(), Version: 1, Enabled: true, LocalRoot: "/owned/source", TargetRootPath: "/owned/target", TargetMediaType: "tv"}
	repo := &reviewScopeRepo{untrackedReviewTestRepo: f.repo, page: ports.UntrackedReviewScopePage{Items: []ports.UntrackedReviewScopeOption{item}}}
	s := NewUntrackedReviewService(repo, f.files)
	page, err := s.ScopeOptions(t.Context(), f.actor, "")
	if err != nil || len(page.Items) != 1 || page.Items[0] != item || repo.calls != 1 || f.files.opens.Load() != 0 {
		t.Fatal(page, err)
	}
	page.Items[0].LocalRoot = "/mutated"
	if repo.page.Items[0].LocalRoot != "/owned/source" {
		t.Fatal("page aliases repository")
	}
	for _, which := range []string{"nil", "duplicate", "cursor", "type", "uuid"} {
		t.Run(which, func(t *testing.T) {
			repo.page = ports.UntrackedReviewScopePage{Items: []ports.UntrackedReviewScopeOption{item}}
			switch which {
			case "nil":
				repo.page.Items = nil
			case "duplicate":
				repo.page.Items = append(repo.page.Items, item)
			case "cursor":
				repo.page.HasMore = true
				repo.page.NextCursor = item.ID
			case "type":
				repo.page.Items[0].TargetMediaType = "arbitrary"
			case "uuid":
				repo.page.Items[0].ID = uuid.Nil.String()
			}
			if _, err := s.ScopeOptions(t.Context(), f.actor, ""); !errors.Is(err, ports.ErrUnavailable) {
				t.Fatal(err)
			}
		})
	}
	before := repo.calls
	if _, err := s.ScopeOptions(t.Context(), nil, ""); !errors.Is(err, ports.ErrUnauthorized) {
		t.Fatal(err)
	}
	member := *f.actor
	member.Role = "member"
	if _, err := s.ScopeOptions(t.Context(), &member, ""); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := s.ScopeOptions(t.Context(), f.actor, "bad"); !errors.Is(err, ports.ErrRejected) {
		t.Fatal(err)
	}
	if repo.calls != before || f.files.opens.Load() != 0 {
		t.Fatal("unauthorized scope options reached repository or filesystem")
	}
}
