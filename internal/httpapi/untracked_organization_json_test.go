package httpapi

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/google/uuid"
)

func TestUntrackedOrganizationHTTPSubjectsBeforeTransport(t *testing.T) {
	for _, subject := range []string{"anonymous", "member", "token", "system", "service", "empty-scopes", "scopes", "invalid-id", "zero-id", "compact-id", "uppercase-id"} {
		t.Run(subject, func(t *testing.T) {
			actor, status := untrackedScopeAdmin(), 403
			switch subject {
			case "anonymous":
				actor, status = nil, 401
			case "member":
				actor.Role = ports.RoleMember
			case "token":
				actor.Type = "api_token"
			case "system":
				actor.Type = ports.ActorTypeSystem
			case "service":
				actor.Type = "service"
			case "empty-scopes":
				actor.Scopes = []string{}
			case "scopes":
				actor.Scopes = []string{"*"}
			case "invalid-id":
				actor.ID = "bad"
			case "zero-id":
				actor.ID = uuid.Nil.String()
			case "compact-id":
				actor.ID = strings.ReplaceAll(actor.ID, "-", "")
			case "uppercase-id":
				actor.ID = "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"
			}
			s, f := untrackedOrganizationHTTPFixture()
			h := untrackedReviewHTTPRouter(s, actor)
			for _, route := range untrackedOrganizationRoutes {
				path := strings.Replace(untrackedReviewPath, untrackedReviewFileID, "bad", 1) + route.suffix + "?"
				w := untrackedScopeCall(h, route.method, path, "not-json", "invalid key")
				untrackedReviewHTTPStatus(t, w, status)
			}
			if f.calls() != 0 {
				t.Fatal("subject rejection reached service", f)
			}
		})
	}
}

func TestUntrackedOrganizationHTTPStrictJSON(t *testing.T) {
	for i, body := range []string{
		"", "null", "{}", "[]", `"text"`, "true", "1",
		`{"confirmation_id":null}`, `{"confirmation_id":0}`, `{"confirmation_id":true}`, `{"confirmation_id":{}}`, `{"confirmation_id":[]}`,
		`{"confirmation_id":""}`, `{"confirmation_id":"bad"}`,
		`{"confirmation_id":"00000000-0000-0000-0000-000000000000"}`,
		`{"confirmation_id":"EEEEEEEE-EEEE-4EEE-8EEE-EEEEEEEEEEEE"}`,
		`{"confirmation_id":"eeeeeeeeeeee4eee8eeeeeeeeeeeeeee"}`,
		`{"confirmation_id":"{eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee}"}`,
		`{"confirmation_id":"urn:uuid:eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"}`,
		`{"Confirmation_id":"` + untrackedOrganizationConfirmationID + `"}`,
		`{"confirmation_id":"` + untrackedOrganizationConfirmationID + `","confirmation_id":"` + untrackedOrganizationConfirmationID + `"}`,
		`{"confirmation_id":"` + untrackedOrganizationConfirmationID + `","confirmation_\u0069d":"` + untrackedOrganizationConfirmationID + `"}`,
		`{"confirmation_id":"` + untrackedOrganizationConfirmationID + `","Confirmation_id":"` + untrackedOrganizationConfirmationID + `"}`,
		`{"confirmation_id":"` + untrackedOrganizationConfirmationID + `","action":"move"}`,
		`{"confirmation_id":"` + untrackedOrganizationConfirmationID + `","target_relative_path":"elsewhere.mkv"}`,
		`{"confirmation_id":"` + untrackedOrganizationConfirmationID + `","operation_id":"` + untrackedOrganizationID + `"}`,
		untrackedOrganizationBody + `{}`, untrackedOrganizationBody + ` null`, untrackedOrganizationBody + ` garbage`,
		untrackedOrganizationBody[:len(untrackedOrganizationBody)-1] + `,}`,
		untrackedOrganizationBody + strings.Repeat(" ", 4096-len(untrackedOrganizationBody)),
	} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s, f := untrackedOrganizationHTTPFixture()
			w := untrackedScopeCall(untrackedReviewHTTPRouter(s, untrackedScopeAdmin()), "POST", untrackedReviewPath+"/organizations", body, untrackedOrganizationKey)
			untrackedReviewHTTPStatus(t, w, 400)
			if f.calls() != 0 {
				t.Fatal("invalid JSON reached service", f)
			}
		})
	}
	for _, size := range []int{len(untrackedOrganizationBody), 4095} {
		s, f := untrackedOrganizationHTTPFixture()
		body := untrackedOrganizationBody + strings.Repeat(" ", size-len(untrackedOrganizationBody))
		w := untrackedScopeCall(untrackedReviewHTTPRouter(s, untrackedScopeAdmin()), "POST", untrackedReviewPath+"/organizations", body, untrackedOrganizationKey)
		untrackedReviewHTTPStatus(t, w, 202)
		if f.calls() != 1 || f.input.ConfirmationID != uuid.MustParse(untrackedOrganizationConfirmationID) {
			t.Fatal("valid boundary rejected or remapped", f)
		}
	}
}

func TestUntrackedOrganizationHTTPStrictTransport(t *testing.T) {
	for index, route := range untrackedOrganizationRoutes {
		for _, mutation := range []string{"query", "bare-query", "scope-zero", "scope-compact", "file-zero", "file-uppercase", "extra-path", "wrong-method", "key", "duplicate-key", "empty-key", "long-key", "invalid-key", "joined-key", "body", "content-type", "missing-content-type", "duplicate-content-type", "operation-zero", "operation-uppercase", "command-key"} {
			t.Run(fmt.Sprintf("%d/%s", index, mutation), func(t *testing.T) {
				s, f := untrackedOrganizationHTTPFixture()
				path, method, body := untrackedReviewPath+route.suffix, route.method, route.body
				switch mutation {
				case "query":
					path += "?a=b"
				case "bare-query":
					path += "?"
				case "scope-zero":
					path = strings.Replace(path, untrackedScopeID, uuid.Nil.String(), 1)
				case "scope-compact":
					path = strings.Replace(path, untrackedScopeID, strings.ReplaceAll(untrackedScopeID, "-", ""), 1)
				case "file-zero":
					path = strings.Replace(path, untrackedReviewFileID, uuid.Nil.String(), 1)
				case "file-uppercase":
					path = strings.Replace(path, untrackedReviewFileID, strings.ToUpper(untrackedReviewFileID), 1)
				case "extra-path":
					path += "/extra"
				case "wrong-method":
					method = "PUT"
				case "body":
					if index == 0 {
						body = "[]"
					} else {
						body = " "
					}
				case "content-type", "missing-content-type", "duplicate-content-type":
					if index != 0 {
						return
					}
				case "operation-zero", "operation-uppercase":
					if index != 1 {
						return
					}
					replacement := uuid.Nil.String()
					if mutation == "operation-uppercase" {
						replacement = strings.ToUpper(untrackedOrganizationID)
					}
					path = strings.Replace(path, untrackedOrganizationID, replacement, 1)
				case "command-key":
					if index != 2 {
						return
					}
					path = strings.Replace(path, untrackedOrganizationKey, "bad%20key", 1)
				}
				r := httptest.NewRequest(method, path, strings.NewReader(body))
				r.Header.Set("Content-Type", "application/json")
				if route.key != "" {
					r.Header.Set("Idempotency-Key", route.key)
				}
				switch mutation {
				case "key":
					if index == 0 {
						r.Header.Del("Idempotency-Key")
					} else {
						r.Header.Set("Idempotency-Key", untrackedOrganizationKey)
					}
				case "duplicate-key":
					r.Header.Add("Idempotency-Key", untrackedOrganizationKey)
					r.Header.Add("Idempotency-Key", untrackedOrganizationKey)
				case "empty-key":
					r.Header.Set("Idempotency-Key", "")
				case "long-key":
					r.Header.Set("Idempotency-Key", strings.Repeat("a", 161))
				case "invalid-key":
					r.Header.Set("Idempotency-Key", "invalid key")
				case "joined-key":
					r.Header.Set("Idempotency-Key", untrackedOrganizationKey+",other")
				case "content-type":
					r.Header.Set("Content-Type", "text/plain")
				case "missing-content-type":
					r.Header.Del("Content-Type")
				case "duplicate-content-type":
					r.Header.Add("Content-Type", "application/json")
				}
				w := httptest.NewRecorder()
				untrackedReviewHTTPRouter(s, untrackedScopeAdmin()).ServeHTTP(w, r)
				untrackedReviewHTTPStatus(t, w, 400)
				if f.calls() != 0 {
					t.Fatal("invalid transport reached service", f)
				}
			})
		}
	}
}

type untrackedOrganizationBrokenBody struct{}

func (untrackedOrganizationBrokenBody) Read([]byte) (int, error) {
	return 0, errors.New("secret body failure")
}
func (untrackedOrganizationBrokenBody) Close() error { return nil }

func TestUntrackedOrganizationHTTPBodyReadFailure(t *testing.T) {
	for _, route := range untrackedOrganizationRoutes {
		s, f := untrackedOrganizationHTTPFixture()
		r := httptest.NewRequest(route.method, untrackedReviewPath+route.suffix, nil)
		r.Body = untrackedOrganizationBrokenBody{}
		r.Header.Set("Content-Type", "application/json")
		if route.key != "" {
			r.Header.Set("Idempotency-Key", route.key)
		}
		w := httptest.NewRecorder()
		untrackedReviewHTTPRouter(s, untrackedScopeAdmin()).ServeHTTP(w, r)
		untrackedReviewHTTPStatus(t, w, 400)
		if f.calls() != 0 || strings.Contains(w.Body.String(), "secret") {
			t.Fatal("body read failure dispatch/disclosure")
		}
	}
}

func TestUntrackedOrganizationHTTPProductionCookieGuards(t *testing.T) {
	for _, test := range []struct {
		name                                                                   string
		member, token, unavailable, missingOrigin, anonymous, invalid, foreign bool
		status                                                                 int
	}{
		{name: "cookie-admin", status: 200}, {name: "cookie-member", member: true, status: 403},
		{name: "admin-token", token: true, status: 403}, {name: "auth-unavailable", unavailable: true, status: 503},
		{name: "csrf", missingOrigin: true, status: 403}, {name: "anonymous-before-binder", anonymous: true, invalid: true, status: 401},
		{name: "member-before-binder", member: true, invalid: true, status: 403}, {name: "admin-bad-uuid", invalid: true, status: 400},
		{name: "foreign-owner", foreign: true, status: 403},
	} {
		for index, route := range untrackedOrganizationRoutes {
			if test.missingOrigin && index != 0 {
				continue
			}
			t.Run(fmt.Sprintf("%s/%d", test.name, index), func(t *testing.T) {
				s, f := untrackedOrganizationHTTPFixture()
				clock := untrackedReviewRouterApp{}.Clock()
				secrets := crypto.AuthenticationSecrets{}
				credential := "organization-session-fixture"
				user := ports.User{ID: uuid.MustParse(untrackedScopeAdmin().ID), Role: ports.RoleAdmin, IsActive: true}
				if test.member {
					user.Role = ports.RoleMember
				}
				repo := &checkinConfigAuthRepo{hash: secrets.HashToken(credential), user: user, expires: clock.Now().Add(time.Hour), token: test.token}
				if test.unavailable {
					repo.err = errors.New("secret DSN cookie")
				}
				if test.foreign {
					f.err = ports.ErrForbidden
				}
				h := NewRouter(untrackedReviewRouterApp{}, s, auth.NewService(repo, secrets, clock, nil), nil)
				path := untrackedReviewPath + route.suffix
				if test.invalid {
					path = strings.Replace(path, untrackedReviewFileID, "bad", 1)
				}
				r := httptest.NewRequest(route.method, path, strings.NewReader(route.body))
				r.Header.Set("Content-Type", "application/json")
				if route.key != "" {
					r.Header.Set("Idempotency-Key", route.key)
				}
				if !test.missingOrigin {
					r.Header.Set("Origin", "http://example.com")
				}
				if !test.anonymous {
					if test.token {
						r.Header.Set("Authorization", "Bearer "+credential)
					} else {
						r.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: credential})
					}
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				status := test.status
				if status == 200 && index == 0 {
					status = 202
				}
				untrackedReviewHTTPStatus(t, w, status)
				response := w.Result()
				if response.Header.Get("Cache-Control") != "private, no-store" {
					t.Fatal("no-store was absent when headers were written")
				}
				_ = response.Body.Close()
				calls := 0
				if status < 300 || test.foreign {
					calls = 1
				}
				if f.calls() != calls || strings.Contains(w.Body.String(), "secret") {
					t.Fatal("production guard dispatch/disclosure", f, w.Body.String())
				}
				if f.calls() == 1 && (!reflectOrganizationActor(f.actor, user) || f.scopeID != f.receipt.Operation.ScopeID || f.fileID != f.receipt.Operation.FileID) {
					t.Fatal("Cookie actor identity was not forwarded", f)
				}
			})
		}
	}
}

func reflectOrganizationActor(actor ports.Actor, user ports.User) bool {
	return actor.ID == user.ID.String() && actor.Type == ports.ActorTypeUser && actor.Role == ports.RoleAdmin && actor.Scopes == nil
}

var _ io.ReadCloser = untrackedOrganizationBrokenBody{}
