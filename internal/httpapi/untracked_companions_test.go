package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var companionTestOp = uuid.MustParse("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
var companionTestID = uuid.MustParse("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
var companionTestKey = strings.Repeat("a", 64)

type companionHTTPFake struct{ calls int }

func (f *companionHTTPFake) item() ports.CompanionAttachment {
	return ports.CompanionAttachment{ID: companionTestID, OperationID: companionTestOp, Candidate: ports.CompanionCandidate{Key: companionTestKey, SourceRelativePath: "Movie.en.srt", TargetRelativePath: "Movie/Movie.en.srt", Identity: ports.FileIdentity{Device: 123, Inode: 456, Size: 12, Mtime: 34}}, Action: "copy", State: "pending", SourceRetained: true}
}
func (f *companionHTTPFake) Discover(context.Context, *ports.Actor, uuid.UUID) ([]ports.CompanionCandidate, error) {
	f.calls++
	return []ports.CompanionCandidate{f.item().Candidate}, nil
}
func (f *companionHTTPFake) Read(context.Context, *ports.Actor, uuid.UUID) (*ports.CompanionView, error) {
	f.calls++
	return &ports.CompanionView{VideoState: "completed", Items: []ports.CompanionAttachment{f.item()}}, nil
}
func (f *companionHTTPFake) Select(context.Context, *ports.Actor, uuid.UUID, string, ports.UntrackedCompanionSelection) (*ports.CompanionView, error) {
	f.calls++
	return &ports.CompanionView{VideoState: "completed", Items: []ports.CompanionAttachment{f.item()}}, nil
}
func (f *companionHTTPFake) Execute(context.Context, *ports.Actor, uuid.UUID, uuid.UUID) (*ports.CompanionAttachment, error) {
	f.calls++
	item := f.item()
	item.State = "completed"
	return &item, nil
}
func companionTestRouter(a *ports.Actor, f *companionHTTPFake) http.Handler {
	s := &ServerHandler{}
	s.SetUntrackedCompanionsService(f)
	h := StrictUntrackedCompanionJSON(NewRouter(untrackedReviewRouterApp{}, s, nil, nil))
	return UntrackedCompanionNoStore(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a != nil {
			r = r.WithContext(context.WithValue(r.Context(), actorKey, a))
		}
		h.ServeHTTP(w, r)
	}))
}
func TestUntrackedCompanionHTTPRoutesAndPrivateIdentity(t *testing.T) {
	base := untrackedCompanionPrefix + companionTestOp.String() + "/companions"
	for _, route := range []struct{ method, suffix, body, key string }{{"GET", "", "", ""}, {"POST", "/discover", "", ""}, {"POST", "", `{"candidate_keys":["` + companionTestKey + `"],"action":"copy"}`, "selection"}, {"POST", "/" + companionTestID.String() + "/execute", "", ""}} {
		t.Run(route.method+route.suffix, func(t *testing.T) {
			f := &companionHTTPFake{}
			keys := []string{}
			if route.key != "" {
				keys = append(keys, route.key)
			}
			w := untrackedScopeCall(companionTestRouter(untrackedScopeAdmin(), f), route.method, base+route.suffix, route.body, keys...)
			if w.Code != 200 || f.calls != 1 {
				t.Fatalf("production route status=%d calls=%d body=%s", w.Code, f.calls, w.Body.String())
			}
			if w.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatal("private response cache policy missing")
			}
			var document any
			if json.Unmarshal(w.Body.Bytes(), &document) != nil {
				t.Fatal("invalid JSON")
			}
			if strings.Contains(w.Body.String(), "identity") || strings.Contains(w.Body.String(), "Device") {
				t.Fatal("physical identity leaked to frontend")
			}
		})
	}
}
func TestUntrackedCompanionHTTPRejectsSubjectsBeforeTransport(t *testing.T) {
	for _, kind := range []string{"anonymous", "member", "token", "scoped"} {
		t.Run(kind, func(t *testing.T) {
			a := untrackedScopeAdmin()
			status := 403
			switch kind {
			case "anonymous":
				a = nil
				status = 401
			case "member":
				a.Role = ports.RoleMember
			case "token":
				a.Type = "api_token"
			case "scoped":
				a.Scopes = []string{}
			}
			f := &companionHTTPFake{}
			w := untrackedScopeCall(companionTestRouter(a, f), "POST", untrackedCompanionPrefix+"invalid/companions?", "bad-json", "bad key")
			if w.Code != status || f.calls != 0 || w.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatalf("subject status %d calls %d headers %v", w.Code, f.calls, w.Header())
			}
		})
	}
}
func TestUntrackedCompanionHTTPStrictTransport(t *testing.T) {
	base := untrackedCompanionPrefix + companionTestOp.String() + "/companions"
	valid := `{"candidate_keys":["` + companionTestKey + `"],"action":"copy"}`
	for i, body := range []string{"", "null", "{}", "[]", valid + "{}", `{"candidate_keys":null,"action":"copy"}`, strings.Replace(valid, `"copy"`, `"move"`, 1), strings.Replace(valid, `"action":"copy"`, `"action":"copy","action":"hardlink"`, 1), strings.Replace(valid, `"action":"copy"`, `"action":"copy","extra":true`, 1), strings.Replace(valid, companionTestKey, strings.ToUpper(companionTestKey), 1), `{"candidate_keys":["` + companionTestKey + `","` + companionTestKey + `"],"action":"copy"}`} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			f := &companionHTTPFake{}
			w := untrackedScopeCall(companionTestRouter(untrackedScopeAdmin(), f), "POST", base, body, "selection")
			if w.Code != 400 || f.calls != 0 {
				t.Fatalf("bad selection reached service %d %d", w.Code, f.calls)
			}
		})
	}
	for _, mutation := range []string{"query", "bare-query", "body", "key", "duplicate-key", "extra-path", "wrong-method", "uppercase-id"} {
		t.Run(mutation, func(t *testing.T) {
			request := httptest.NewRequest("POST", base+"/discover", nil)
			switch mutation {
			case "query":
				request.URL.RawQuery = "a=b"
			case "bare-query":
				request.URL.ForceQuery = true
			case "body":
				request = httptest.NewRequest("POST", base+"/discover", strings.NewReader(" "))
			case "key":
				request.Header.Set("Idempotency-Key", "forbidden")
			case "duplicate-key":
				request = httptest.NewRequest("POST", base, strings.NewReader(valid))
				request.Header.Set("Content-Type", "application/json")
				request.Header.Add("Idempotency-Key", "one")
				request.Header.Add("Idempotency-Key", "two")
			case "extra-path":
				request.URL.Path += "/extra"
			case "wrong-method":
				request.Method = "DELETE"
			case "uppercase-id":
				request.URL.Path = strings.Replace(request.URL.Path, companionTestOp.String(), strings.ToUpper(companionTestOp.String()), 1)
			}
			f := &companionHTTPFake{}
			w := httptest.NewRecorder()
			companionTestRouter(untrackedScopeAdmin(), f).ServeHTTP(w, request)
			if w.Code != 400 || f.calls != 0 {
				t.Fatalf("invalid transport reached service %d %d", w.Code, f.calls)
			}
		})
	}
}
