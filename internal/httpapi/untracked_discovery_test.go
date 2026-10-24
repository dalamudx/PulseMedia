package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type discoveryHTTPFake struct {
	page   ports.UntrackedDiscoveryPage
	err    error
	calls  int
	scope  uuid.UUID
	cursor string
	limit  int
}

func (f *discoveryHTTPFake) Read(_ context.Context, _ *ports.Actor, id uuid.UUID, c string, n int) (ports.UntrackedDiscoveryPage, error) {
	f.calls++
	f.scope, f.cursor, f.limit = id, c, n
	return f.page, f.err
}
func TestUntrackedDiscoveryHTTPReadOnlyContract(t *testing.T) {
	stamp := time.Now().UTC()
	id := uuid.MustParse(untrackedScopeID)
	fake := &discoveryHTTPFake{page: ports.UntrackedDiscoveryPage{Files: []ports.UntrackedDiscoveredFile{{ID: uuid.New(), ScopeID: id, ScopeVersion: 2, Revision: 3, RelativePath: "folder/Movie.mkv", Identity: ports.FileIdentity{Device: 4, Inode: 5, Size: 6, Mtime: 7}, FirstObservedAt: stamp, LastObservedAt: stamp, Observations: 1}}, Complete: true}}
	s := &ServerHandler{}
	s.SetUntrackedDiscoveryService(fake)
	h := untrackedScopeRouter(s, untrackedScopeAdmin())
	w := untrackedScopeCall(h, "GET", untrackedScopePath+"/discoveries?limit=1&cursor=opaque", "")
	untrackedScopeStatus(t, w, http.StatusOK)
	var out UntrackedDiscoveryList
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if fake.calls != 1 || fake.scope != id || fake.cursor != "opaque" || fake.limit != 1 || len(out.Items) != 1 || bool(out.InventoryComplete) || bool(out.ExecutionAllowed) || out.HasMore || out.Items[0].RelativePath != "folder/Movie.mkv" || out.Items[0].Revision != 3 {
		t.Fatal(out, fake)
	}
}
func TestUntrackedDiscoveryHTTPSubjects(t *testing.T) {
	for _, kind := range []string{"anonymous", "member", "token", "service"} {
		t.Run(kind, func(t *testing.T) {
			actor := untrackedScopeAdmin()
			status := http.StatusForbidden
			switch kind {
			case "anonymous":
				actor = nil
				status = http.StatusUnauthorized
			case "member":
				actor.Role = ports.RoleMember
			case "token":
				actor.Scopes = []string{}
			case "service":
				actor.Type = ports.ActorTypeService
			}
			fake := &discoveryHTTPFake{}
			s := &ServerHandler{}
			s.SetUntrackedDiscoveryService(fake)
			w := untrackedScopeCall(untrackedScopeRouter(s, actor), "GET", untrackedScopePath+"/discoveries", "")
			untrackedScopeStatus(t, w, status)
			if fake.calls != 0 {
				t.Fatal("forbidden read entered service")
			}
		})
	}
}
func TestUntrackedDiscoveryHTTPStrictTransport(t *testing.T) {
	for _, test := range []struct{ name, suffix, body, key string }{
		{"zero", "?limit=0", "", ""}, {"large", "?limit=65", "", ""}, {"duplicate", "?limit=1&limit=2", "", ""}, {"unknown", "?extra=true", "", ""}, {"bare", "?", "", ""}, {"body", "", "{}", ""}, {"key", "", "", "read-key"}, {"cursor", "?cursor=" + strings.Repeat("x", 2049), "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := &discoveryHTTPFake{}
			s := &ServerHandler{}
			s.SetUntrackedDiscoveryService(fake)
			var keys []string
			if test.key != "" {
				keys = append(keys, test.key)
			}
			w := untrackedScopeCall(untrackedScopeRouter(s, untrackedScopeAdmin()), "GET", untrackedScopePath+"/discoveries"+test.suffix, test.body, keys...)
			untrackedScopeStatus(t, w, http.StatusBadRequest)
			if fake.calls != 0 {
				t.Fatal("invalid transport reached service")
			}
		})
	}
}
func TestUntrackedDiscoveryHTTPSafeFailure(t *testing.T) {
	fake := &discoveryHTTPFake{err: errors.New("secret DSN /private/source")}
	s := &ServerHandler{}
	s.SetUntrackedDiscoveryService(fake)
	w := untrackedScopeCall(untrackedScopeRouter(s, untrackedScopeAdmin()), "GET", untrackedScopePath+"/discoveries", "")
	untrackedScopeStatus(t, w, http.StatusServiceUnavailable)
	if strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "DSN") {
		t.Fatal("failure disclosed execution material")
	}
	s.SetUntrackedDiscoveryService(nil)
	w = untrackedScopeCall(untrackedScopeRouter(s, untrackedScopeAdmin()), "GET", untrackedScopePath+"/discoveries", "")
	untrackedScopeStatus(t, w, http.StatusServiceUnavailable)
}
