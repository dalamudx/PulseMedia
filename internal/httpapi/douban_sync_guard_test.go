package httpapi

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLegacyDoubanSyncCannotPublishUnverifiedIdentity(t *testing.T) {
	for _, role := range []string{"anonymous", ports.RoleMember, ports.RoleAdmin} {
		t.Run(role, func(t *testing.T) {
			ctx := context.Background()
			if role != "anonymous" {
				ctx = context.WithValue(ctx, actorKey, &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: role})
			}
			// No database or transport is provided: this entry must not use either.
			result, err := (&ServerHandler{}).SyncDoubanWishlist(ctx, SyncDoubanWishlistRequestObject{})
			if err != nil {
				t.Fatal(err)
			}
			recorder := httptest.NewRecorder()
			if err = result.VisitSyncDoubanWishlistResponse(recorder); err != nil {
				t.Fatal(err)
			}
			want := http.StatusServiceUnavailable
			if role == "anonymous" {
				want = http.StatusUnauthorized
			}
			if recorder.Code != want {
				t.Fatalf("status=%d want=%d", recorder.Code, want)
			}
			if role != "anonymous" && (!strings.Contains(recorder.Body.String(), "DOUBAN_SYNC_UNAVAILABLE") || !strings.Contains(recorder.Header().Get("Content-Type"), "application/problem+json")) {
				t.Fatal("missing explicit unavailable response")
			}
		})
	}
}
