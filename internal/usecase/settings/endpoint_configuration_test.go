package settings_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/settings"
	"github.com/google/uuid"
)

type endpointConfigurationRepo struct {
	calls     []string
	actor     *ports.Actor
	family    ports.EndpointFamily
	id        uuid.UUID
	input     ports.EndpointConfigurationInput
	rows      []ports.EndpointConfiguration
	row       *ports.EndpointConfiguration
	err       error
	scope     *ports.EndpointProbeScope
	authorize func(context.Context) error
	complete  func(context.Context, *ports.Actor, ports.EndpointProbeScope, bool) error
}

func (r *endpointConfigurationRepo) record(name string, actor *ports.Actor, family ports.EndpointFamily, id uuid.UUID) {
	r.calls = append(r.calls, name)
	r.actor, r.family, r.id = actor, family, id
}

func (r *endpointConfigurationRepo) ListEndpointConfigurations(_ context.Context, actor *ports.Actor, family ports.EndpointFamily) ([]ports.EndpointConfiguration, error) {
	r.record("list", actor, family, uuid.Nil)
	return r.rows, r.err
}

func (r *endpointConfigurationRepo) CreateEndpointConfiguration(_ context.Context, actor *ports.Actor, family ports.EndpointFamily, input ports.EndpointConfigurationInput) (*ports.EndpointConfiguration, error) {
	r.record("create", actor, family, uuid.Nil)
	r.input = input
	return r.row, r.err
}

func (r *endpointConfigurationRepo) UpdateEndpointConfiguration(_ context.Context, actor *ports.Actor, family ports.EndpointFamily, id uuid.UUID, input ports.EndpointConfigurationInput) (*ports.EndpointConfiguration, error) {
	r.record("update", actor, family, id)
	r.input = input
	return r.row, r.err
}

func (r *endpointConfigurationRepo) DeleteEndpointConfiguration(_ context.Context, actor *ports.Actor, family ports.EndpointFamily, id uuid.UUID) error {
	r.record("delete", actor, family, id)
	return r.err
}

func (r *endpointConfigurationRepo) AuthorizeEndpointProbe(ctx context.Context, actor *ports.Actor, family ports.EndpointFamily, id uuid.UUID) (*ports.EndpointProbeScope, error) {
	r.record("authorize", actor, family, id)
	if r.authorize != nil {
		if err := r.authorize(ctx); err != nil {
			return nil, err
		}
	}
	return r.scope, r.err
}

func (r *endpointConfigurationRepo) CompleteEndpointProbe(ctx context.Context, actor *ports.Actor, scope ports.EndpointProbeScope, alive bool) error {
	r.calls = append(r.calls, "complete")
	if r.complete == nil {
		return nil
	}
	return r.complete(ctx, actor, scope, alive)
}

type endpointConfigurationTester func(context.Context, uuid.UUID) (bool, string, error)

func (f endpointConfigurationTester) TestClient(ctx context.Context, id uuid.UUID) (bool, string, error) {
	return f(ctx, id)
}

func (f endpointConfigurationTester) TestServer(ctx context.Context, id uuid.UUID) (bool, string, error) {
	return f(ctx, id)
}

func endpointConfigurationFixture() (*ports.Actor, *endpointConfigurationRepo) {
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin}
	scope := &ports.EndpointProbeScope{
		Family: ports.DownloadClientEndpoint, ID: uuid.New(),
		UpdatedAt: time.Date(2026, 10, 19, 12, 0, 0, 123, time.UTC), ConfigStamp: "opaque-version",
	}
	row := &ports.EndpointConfiguration{Family: scope.Family, ID: scope.ID, UpdatedAt: scope.UpdatedAt}
	return actor, &endpointConfigurationRepo{scope: scope, row: row, rows: []ports.EndpointConfiguration{*row}}
}

func TestEndpointConfigurationAuthorizationGates(t *testing.T) {
	for _, name := range []string{"create", "update", "delete", "probe"} {
		t.Run(name, func(t *testing.T) {
			admin, repo := endpointConfigurationFixture()
			io := 0
			tester := endpointConfigurationTester(func(context.Context, uuid.UUID) (bool, string, error) {
				io++
				return true, "", nil
			})
			service := settings.NewEndpointConfigurationService(repo, tester, tester)
			for _, tc := range []struct {
				name  string
				actor *ports.Actor
				want  error
			}{
				{"missing", nil, ports.ErrUnauthorized},
				{"member", &ports.Actor{Type: ports.ActorTypeUser, ID: admin.ID, Role: ports.RoleMember}, ports.ErrForbidden},
				{"empty-token", &ports.Actor{Type: ports.ActorTypeUser, ID: admin.ID, Role: ports.RoleAdmin, Scopes: []string{}}, ports.ErrForbidden},
				{"admin-token", &ports.Actor{Type: ports.ActorTypeUser, ID: admin.ID, Role: ports.RoleAdmin, Scopes: []string{"admin"}}, ports.ErrForbidden},
				{"system", &ports.Actor{Type: ports.ActorTypeSystem, ID: admin.ID, Role: ports.RoleAdmin}, ports.ErrForbidden},
				{"service", &ports.Actor{Type: ports.ActorTypeService, ID: admin.ID, Role: ports.RoleAdmin}, ports.ErrForbidden},
				{"unknown-role", &ports.Actor{Type: ports.ActorTypeUser, ID: admin.ID, Role: "owner"}, ports.ErrForbidden},
				{"bad-user-id", &ports.Actor{Type: ports.ActorTypeUser, ID: "not-a-uuid", Role: ports.RoleAdmin}, ports.ErrInvalidEndpointConfiguration},
				{"zero-user-id", &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.Nil.String(), Role: ports.RoleAdmin}, ports.ErrInvalidEndpointConfiguration},
			} {
				t.Run(tc.name, func(t *testing.T) {
					input := ports.EndpointConfigurationInput{ExpectedUpdatedAt: repo.scope.UpdatedAt}
					var err error
					switch name {
					case "create":
						_, err = service.Create(context.Background(), tc.actor, repo.scope.Family, ports.EndpointConfigurationInput{})
					case "update":
						_, err = service.Update(context.Background(), tc.actor, repo.scope.Family, repo.scope.ID, input)
					case "delete":
						err = service.Delete(context.Background(), tc.actor, repo.scope.Family, repo.scope.ID)
					case "probe":
						_, err = service.Probe(context.Background(), tc.actor, repo.scope.Family, repo.scope.ID)
					}
					if !errors.Is(err, tc.want) || len(repo.calls) != 0 || io != 0 {
						t.Fatalf("err=%v want=%v calls=%v I/O=%d", err, tc.want, repo.calls, io)
					}
				})
			}
		})
	}
}

func TestEndpointConfigurationListAccountAndReadToken(t *testing.T) {
	for _, role := range []string{ports.RoleAdmin, ports.RoleMember} {
		for _, token := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/token=%t", role, token), func(t *testing.T) {
				actor, repo := endpointConfigurationFixture()
				actor.Role = role
				if token {
					actor.Scopes = []string{"read"}
				}
				service := settings.NewEndpointConfigurationService(repo, nil, nil)
				rows, err := service.List(context.Background(), actor, ports.MediaServerEndpoint)
				if err != nil || !reflect.DeepEqual(rows, repo.rows) || repo.actor != actor || repo.family != ports.MediaServerEndpoint {
					t.Fatalf("list projection/identity changed: rows=%v err=%v", rows, err)
				}
				repo.err = ports.ErrUserDisabled
				if _, err := service.List(context.Background(), actor, ports.MediaServerEndpoint); !errors.Is(err, repo.err) {
					t.Fatalf("current database account rejection lost: %v", err)
				}
			})
		}
	}
	actor, repo := endpointConfigurationFixture()
	for _, bad := range []*ports.Actor{nil, {Type: ports.ActorTypeSystem, ID: actor.ID, Role: ports.RoleAdmin}, {Type: ports.ActorTypeUser, ID: "bad", Role: ports.RoleMember}, {Type: ports.ActorTypeUser, ID: actor.ID, Role: "unknown"}} {
		if _, err := settings.NewEndpointConfigurationService(repo, nil, nil).List(context.Background(), bad, repo.scope.Family); err == nil || len(repo.calls) != 0 {
			t.Fatalf("invalid list identity reached repository: err=%v calls=%v", err, repo.calls)
		}
	}
}

func TestEndpointConfigurationInputsAndForwarding(t *testing.T) {
	for _, family := range []ports.EndpointFamily{ports.DownloadClientEndpoint, ports.MediaServerEndpoint} {
		t.Run(string(family), func(t *testing.T) {
			actor, repo := endpointConfigurationFixture()
			service := settings.NewEndpointConfigurationService(repo, nil, nil)
			ctx := context.Background()
			input := ports.EndpointConfigurationInput{Name: "endpoint", Protocol: "native", URL: "http://localhost/path?secret=value", Username: "user", Secret: "password", Mapping: json.RawMessage(`{"root":"/media"}`)}
			row, err := service.Create(ctx, actor, family, input)
			if err != nil || row != repo.row || !reflect.DeepEqual(repo.input, input) || repo.actor != actor || repo.family != family {
				t.Fatalf("create input/projection not forwarded: %v", err)
			}
			input.Mapping = nil
			input.ExpectedUpdatedAt = repo.scope.UpdatedAt
			row, err = service.Update(ctx, actor, family, repo.scope.ID, input)
			if err != nil || row != repo.row || !reflect.DeepEqual(repo.input, input) || repo.id != repo.scope.ID || repo.family != family {
				t.Fatalf("update input/version not forwarded: %v", err)
			}
			if err := service.Delete(ctx, actor, family, repo.scope.ID); err != nil || repo.id != repo.scope.ID || repo.family != family {
				t.Fatalf("delete scope not forwarded: %v", err)
			}
			repo.calls = nil
			for _, url := range []string{"", "http://localhost", "http://localhost/full/path?credential=secret"} {
				input.URL, input.ExpectedUpdatedAt = url, time.Time{}
				if _, err := service.Update(ctx, actor, family, repo.scope.ID, input); !errors.Is(err, ports.ErrInvalidEndpointConfiguration) {
					t.Fatalf("URL %q bypassed required version: %v", url, err)
				}
			}
			input.ExpectedUpdatedAt = repo.scope.UpdatedAt
			for _, mapping := range []json.RawMessage{json.RawMessage(`{}`), json.RawMessage(`null`), json.RawMessage(` `)} {
				input.Mapping = mapping
				if _, err := service.Update(ctx, actor, family, repo.scope.ID, input); !errors.Is(err, ports.ErrInvalidEndpointConfiguration) {
					t.Fatalf("mapping update silently ignored: %v", err)
				}
			}
			if _, err := service.Create(ctx, actor, family, input); !errors.Is(err, ports.ErrInvalidEndpointConfiguration) {
				t.Fatalf("create silently accepted expected update version: %v", err)
			}
			if len(repo.calls) != 0 {
				t.Fatalf("invalid input reached repository: %v", repo.calls)
			}
		})
	}
}

func TestEndpointConfigurationInvalidFamilyIDAndNilRepository(t *testing.T) {
	actor, repo := endpointConfigurationFixture()
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		service *settings.EndpointConfigurationService
		family  ports.EndpointFamily
		want    error
	}{
		{"unknown-family", settings.NewEndpointConfigurationService(repo, nil, nil), "other", ports.ErrInvalidEndpointConfiguration},
		{"empty-family", settings.NewEndpointConfigurationService(repo, nil, nil), "", ports.ErrInvalidEndpointConfiguration},
		{"nil-repo", settings.NewEndpointConfigurationService(nil, nil, nil), repo.scope.Family, ports.ErrUnavailable},
		{"nil-service", nil, repo.scope.Family, ports.ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, listErr := tc.service.List(ctx, actor, tc.family)
			_, createErr := tc.service.Create(ctx, actor, tc.family, ports.EndpointConfigurationInput{})
			_, updateErr := tc.service.Update(ctx, actor, tc.family, repo.scope.ID, ports.EndpointConfigurationInput{ExpectedUpdatedAt: repo.scope.UpdatedAt})
			deleteErr := tc.service.Delete(ctx, actor, tc.family, repo.scope.ID)
			_, probeErr := tc.service.Probe(ctx, actor, tc.family, repo.scope.ID)
			for _, err := range []error{listErr, createErr, updateErr, deleteErr, probeErr} {
				if !errors.Is(err, tc.want) {
					t.Errorf("err=%v want=%v", err, tc.want)
				}
			}
		})
	}
	service := settings.NewEndpointConfigurationService(repo, nil, nil)
	_, updateErr := service.Update(ctx, actor, repo.scope.Family, uuid.Nil, ports.EndpointConfigurationInput{ExpectedUpdatedAt: repo.scope.UpdatedAt})
	deleteErr := service.Delete(ctx, actor, repo.scope.Family, uuid.Nil)
	_, probeErr := service.Probe(ctx, actor, repo.scope.Family, uuid.Nil)
	for _, err := range []error{updateErr, deleteErr, probeErr} {
		if !errors.Is(err, ports.ErrInvalidEndpointConfiguration) {
			t.Errorf("zero endpoint ID: %v", err)
		}
	}
	if len(repo.calls) != 0 {
		t.Fatalf("invalid request reached repository: %v", repo.calls)
	}
}

func TestEndpointConfigurationRepositoryErrors(t *testing.T) {
	actor, repo := endpointConfigurationFixture()
	repo.err = ports.ErrEndpointVersionConflict
	service := settings.NewEndpointConfigurationService(repo, nil, nil)
	ctx := context.Background()
	_, createErr := service.Create(ctx, actor, repo.scope.Family, ports.EndpointConfigurationInput{})
	_, updateErr := service.Update(ctx, actor, repo.scope.Family, repo.scope.ID, ports.EndpointConfigurationInput{ExpectedUpdatedAt: repo.scope.UpdatedAt})
	deleteErr := service.Delete(ctx, actor, repo.scope.Family, repo.scope.ID)
	for _, err := range []error{createErr, updateErr, deleteErr} {
		if !errors.Is(err, repo.err) {
			t.Errorf("repository error changed: %v", err)
		}
	}
}

func TestEndpointConfigurationProbeClassificationAndFamily(t *testing.T) {
	const sensitive = "http://user:password@localhost/private?token=secret"
	for _, family := range []ports.EndpointFamily{ports.DownloadClientEndpoint, ports.MediaServerEndpoint} {
		for _, tc := range []struct {
			name     string
			alive    bool
			testErr  error
			wantErr  error
			wantLive bool
		}{
			{"reachable", true, nil, nil, true},
			{"negative-native-result", false, nil, nil, false},
			{"native-failed", true, fmt.Errorf("%s: %w", sensitive, ports.ErrEndpointProbeFailed), nil, false},
			{"native-timeout", false, fmt.Errorf("%w: %w", ports.ErrEndpointProbeFailed, context.DeadlineExceeded), nil, false},
			{"missing", false, fmt.Errorf("%s: %w", sensitive, ports.ErrNotFound), ports.ErrNotFound, false},
			{"credentials-unavailable", false, fmt.Errorf("%s: %w", sensitive, ports.ErrUnavailable), ports.ErrUnavailable, false},
			{"unexpected", true, errors.New(sensitive), ports.ErrUnavailable, false},
			{"unclassified-timeout", false, context.DeadlineExceeded, ports.ErrUnavailable, false},
			{"unclassified-cancellation", false, context.Canceled, ports.ErrUnavailable, false},
		} {
			t.Run(string(family)+"/"+tc.name, func(t *testing.T) {
				actor, repo := endpointConfigurationFixture()
				repo.scope.Family = family
				observed := *repo.scope
				parent := context.Background()
				io, completions := 0, 0
				tester := endpointConfigurationTester(func(ctx context.Context, id uuid.UUID) (bool, string, error) {
					io++
					if !reflect.DeepEqual(repo.calls, []string{"authorize"}) || id != observed.ID || repo.family != family || repo.actor != actor {
						t.Fatal("probe ran before exact scope authorization")
					}
					deadline, ok := ctx.Deadline()
					if remaining := time.Until(deadline); !ok || remaining <= 0 || remaining > 10*time.Second {
						t.Fatalf("network deadline=%v, present=%t", deadline, ok)
					}
					return tc.alive, sensitive, tc.testErr
				})
				wrong := endpointConfigurationTester(func(context.Context, uuid.UUID) (bool, string, error) {
					t.Fatal("wrong family tester called")
					return false, "", nil
				})
				download, media := tester, wrong
				if family == ports.MediaServerEndpoint {
					download, media = wrong, tester
				}
				repo.complete = func(ctx context.Context, current *ports.Actor, scope ports.EndpointProbeScope, alive bool) error {
					completions++
					if ctx != parent || current != actor || scope != observed || alive != tc.wantLive || io != 1 {
						t.Fatalf("completion lost parent/current actor/scope/result: scope=%v alive=%t", scope, alive)
					}
					return nil
				}
				result, err := settings.NewEndpointConfigurationService(repo, download, media).Probe(parent, actor, family, observed.ID)
				if tc.wantErr != nil {
					if err != tc.wantErr || result != nil || completions != 0 || io != 1 {
						t.Fatalf("unsafe error classification: result=%v err=%v complete=%d I/O=%d", result, err, completions, io)
					}
					return
				}
				message := "endpoint unavailable"
				if tc.wantLive {
					message = "ok"
				}
				if err != nil || result == nil || result.Alive != tc.wantLive || result.Message != message || completions != 1 || io != 1 {
					t.Fatalf("unsafe or incomplete result: result=%v err=%v complete=%d I/O=%d", result, err, completions, io)
				}
			})
		}
	}
}

func TestEndpointConfigurationProbeAuthorizationAndScope(t *testing.T) {
	for _, name := range []string{"denied", "not-found", "storage", "nil-scope", "family", "unknown-family", "id", "time", "stamp", "nil-tester"} {
		t.Run(name, func(t *testing.T) {
			actor, repo := endpointConfigurationFixture()
			family, id := repo.scope.Family, repo.scope.ID
			want := ports.ErrUnavailable
			switch name {
			case "denied":
				repo.err, want = ports.ErrForbidden, ports.ErrForbidden
			case "not-found":
				repo.err, want = ports.ErrNotFound, ports.ErrNotFound
			case "storage":
				repo.err = ports.ErrUnavailable
			case "nil-scope":
				repo.scope = nil
			case "family":
				repo.scope.Family = ports.MediaServerEndpoint
			case "unknown-family":
				repo.scope.Family = "other"
			case "id":
				repo.scope.ID = uuid.New()
			case "time":
				repo.scope.UpdatedAt = time.Time{}
			case "stamp":
				repo.scope.ConfigStamp = ""
			}
			var tester ports.DownloadEndpointTester = endpointConfigurationTester(func(context.Context, uuid.UUID) (bool, string, error) {
				t.Fatal("unauthorized or unavailable probe performed I/O")
				return false, "", nil
			})
			if name == "nil-tester" {
				tester = nil
			}
			result, err := settings.NewEndpointConfigurationService(repo, tester, nil).Probe(context.Background(), actor, family, id)
			if !errors.Is(err, want) || result != nil || !reflect.DeepEqual(repo.calls, []string{"authorize"}) {
				t.Fatalf("result=%v err=%v calls=%v", result, err, repo.calls)
			}
		})
	}
}

func TestEndpointConfigurationProbeCompletionRejectsStaleResult(t *testing.T) {
	for _, rejection := range []error{ports.ErrForbidden, ports.ErrUserDisabled, ports.ErrEndpointVersionConflict, ports.ErrNotFound, ports.ErrUnavailable} {
		for _, nativeErr := range []error{nil, ports.ErrEndpointProbeFailed} {
			t.Run(fmt.Sprintf("%v/native=%v", rejection, nativeErr), func(t *testing.T) {
				actor, repo := endpointConfigurationFixture()
				observed := *repo.scope
				tester := endpointConfigurationTester(func(context.Context, uuid.UUID) (bool, string, error) {
					// A replacement repository projection cannot change the authorized observation.
					repo.scope.ConfigStamp = "replacement"
					return true, "private native diagnostic", nativeErr
				})
				repo.complete = func(_ context.Context, current *ports.Actor, scope ports.EndpointProbeScope, _ bool) error {
					if current != actor || scope != observed {
						t.Fatal("post-I/O recheck used changed observation or wrong account")
					}
					return rejection
				}
				result, err := settings.NewEndpointConfigurationService(repo, tester, nil).Probe(context.Background(), actor, observed.Family, observed.ID)
				if err != rejection || result != nil || !reflect.DeepEqual(repo.calls, []string{"authorize", "complete"}) {
					t.Fatalf("stale result escaped: result=%v err=%v calls=%v", result, err, repo.calls)
				}
			})
		}
	}
}

func TestEndpointConfigurationProbeParentDeadlineAndCancellation(t *testing.T) {
	t.Run("parent-deadline", func(t *testing.T) {
		actor, repo := endpointConfigurationFixture()
		parent, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		var network context.Context
		repo.authorize = func(ctx context.Context) error {
			if ctx != parent {
				t.Fatal("network budget applied to repository")
			}
			return nil
		}
		tester := endpointConfigurationTester(func(ctx context.Context, _ uuid.UUID) (bool, string, error) {
			network = ctx
			deadline, _ := ctx.Deadline()
			original, _ := parent.Deadline()
			if !deadline.Equal(original) {
				t.Fatal("probe extended parent deadline")
			}
			return true, "", nil
		})
		repo.complete = func(ctx context.Context, _ *ports.Actor, _ ports.EndpointProbeScope, _ bool) error {
			if ctx != parent || ctx.Err() != nil || !errors.Is(network.Err(), context.Canceled) {
				t.Fatal("completion reused network context")
			}
			return nil
		}
		if _, err := settings.NewEndpointConfigurationService(repo, tester, nil).Probe(parent, actor, repo.scope.Family, repo.scope.ID); err != nil {
			t.Fatal(err)
		}
	})
	for _, stage := range []string{"before-authorize", "after-authorize", "during-native"} {
		t.Run(stage, func(t *testing.T) {
			actor, repo := endpointConfigurationFixture()
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			tester := endpointConfigurationTester(func(ctx context.Context, _ uuid.UUID) (bool, string, error) {
				close(started)
				<-ctx.Done()
				close(canceled)
				<-release
				return true, "secret", ports.ErrEndpointProbeFailed
			})
			if stage == "before-authorize" {
				cancel()
			}
			if stage == "after-authorize" {
				repo.authorize = func(context.Context) error { cancel(); return nil }
			}
			type outcome struct {
				result *ports.EndpointProbeResult
				err    error
			}
			done := make(chan outcome, 1)
			go func() {
				result, err := settings.NewEndpointConfigurationService(repo, tester, nil).Probe(parent, actor, repo.scope.Family, repo.scope.ID)
				done <- outcome{result, err}
			}()
			if stage == "during-native" {
				select {
				case <-started:
				case <-time.After(3 * time.Second):
					cancel()
					close(release)
					<-done
					t.Fatal("native call did not start")
				}
				cancel()
				<-canceled
				select {
				case early := <-done:
					close(release)
					t.Fatalf("probe returned before physical call joined: %v", early)
				default:
				}
			}
			close(release)
			got := <-done
			if !errors.Is(got.err, context.Canceled) || got.result != nil {
				t.Fatalf("parent cancellation lost: result=%v err=%v", got.result, got.err)
			}
			wantCalls := []string{"authorize"}
			if stage == "before-authorize" {
				wantCalls = nil
			}
			if !reflect.DeepEqual(repo.calls, wantCalls) {
				t.Fatalf("canceled probe committed: %v", repo.calls)
			}
			if stage != "during-native" {
				select {
				case <-started:
					t.Fatal("already canceled probe performed I/O")
				default:
				}
			}
		})
	}
}

func TestEndpointConfigurationProbeRejectsLateSuccess(t *testing.T) {
	actor, repo := endpointConfigurationFixture()
	tester := endpointConfigurationTester(func(ctx context.Context, _ uuid.UUID) (bool, string, error) {
		<-ctx.Done()
		return true, "late success", nil
	})
	service := settings.NewEndpointConfigurationService(repo, tester, nil)
	result, err := service.Probe(context.Background(), actor, ports.DownloadClientEndpoint, repo.scope.ID)
	if result != nil || !errors.Is(err, ports.ErrUnavailable) {
		t.Fatalf("late probe published a result: %+v, %v", result, err)
	}
	if !reflect.DeepEqual(repo.calls, []string{"authorize"}) {
		t.Fatalf("late success committed probe observation: %v", repo.calls)
	}
}
