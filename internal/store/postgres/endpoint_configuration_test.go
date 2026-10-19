package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/endpoint"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type endpointConfigFixture struct {
	p       *pgxpool.Pool
	repo    *postgres.EndpointConfigurationStore
	actor   *ports.Actor
	secrets interface {
		ports.SecretProtector
		DecryptSecret([]byte) ([]byte, error)
	}
}

func newEndpointConfigFixture(t *testing.T) endpointConfigFixture {
	t.Helper()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	m5IndexerSchemaVersion(t, dsn, 91)
	p, err := postgres.NewPool(t.Context(), dsn, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	var version string
	if err = p.QueryRow(t.Context(), "SHOW server_version").Scan(&version); err != nil || !strings.HasPrefix(version, "18.6") {
		t.Fatal("private PG18.6 required")
	}
	secrets := crypto.NewSecretProtector(bytes.Repeat([]byte{51}, 32))
	actor := pgtest.CreateUserActor(t, db.New(p), &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin})
	return endpointConfigFixture{p, postgres.NewEndpointConfigurationStore(p, idgen.New(), secrets), actor, secrets}
}
func endpointConfigInput(f ports.EndpointFamily) ports.EndpointConfigurationInput {
	in := ports.EndpointConfigurationInput{Name: "Configured endpoint", Protocol: "qbittorrent", URL: "http://127.0.0.1:8080/base?token=private-url&category=movie", Username: "native-user", Secret: "native-secret", Mapping: json.RawMessage(`{"/remote":"/local"}`)}
	if f == ports.MediaServerEndpoint {
		in.Protocol = "jellyfin"
		in.Username = ""
	}
	return in
}
func (f endpointConfigFixture) create(t *testing.T, family ports.EndpointFamily) *ports.EndpointConfiguration {
	t.Helper()
	v, e := f.repo.CreateEndpointConfiguration(t.Context(), f.actor, family, endpointConfigInput(family))
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func (f endpointConfigFixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := f.p.Exec(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}
func (f endpointConfigFixture) stored(t *testing.T, family ports.EndpointFamily, id uuid.UUID) (raw string, cipher, mapping []byte, version time.Time) {
	t.Helper()
	if family == ports.DownloadClientEndpoint {
		r, e := db.New(f.p).GetDownloadClientByID(t.Context(), id)
		if e != nil {
			t.Fatal(e)
		}
		return r.Url, r.PasswordEncrypted, r.RemotePathMap, r.UpdatedAt.Time
	}
	r, e := db.New(f.p).GetMediaServerByID(t.Context(), id)
	if e != nil {
		t.Fatal(e)
	}
	return r.Url, r.ApiKeyEncrypted, r.PathMapping, r.UpdatedAt.Time
}
func (f endpointConfigFixture) counts(t *testing.T) (audit, outbox int) {
	t.Helper()
	if e := f.p.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM audit_log),(SELECT count(*) FROM outbox_event)`).Scan(&audit, &outbox); e != nil {
		t.Fatal(e)
	}
	return
}

func TestEndpointConfigurationNativeRoundtripPG(t *testing.T) {
	f := newEndpointConfigFixture(t)
	for _, family := range []ports.EndpointFamily{ports.DownloadClientEndpoint, ports.MediaServerEndpoint} {
		t.Run(string(family), func(t *testing.T) {
			protocols := []string{"qbittorrent", "transmission"}
			if family == ports.MediaServerEndpoint {
				protocols = []string{"jellyfin", "plex", "emby"}
			}
			for _, protocol := range protocols {
				t.Run(protocol, func(t *testing.T) {
					in := endpointConfigInput(family)
					in.Protocol = protocol
					in.URL = "http://user:private@192.168.1.4:8080/base?token=private-url&page=2#private-fragment"
					row, e := f.repo.CreateEndpointConfiguration(t.Context(), f.actor, family, in)
					if e != nil {
						t.Fatal(e)
					}
					raw, cipher, mapping, version := f.stored(t, family, row.ID)
					plain, e := f.secrets.DecryptSecret(cipher)
					if e != nil || string(plain) != in.Secret || raw != in.URL || row.URL != endpoint.DisplayURL(in.URL) {
						t.Fatal("native config did not roundtrip", e)
					}
					encoded, _ := json.Marshal(row)
					if strings.Contains(string(encoded), "private") || strings.Contains(string(encoded), "native-secret") || bytes.Contains(encoded, cipher) {
						t.Fatal("unsafe projection")
					}
					in.Secret = ""
					in.URL = row.URL
					in.Mapping = json.RawMessage(`{"/ignored":"/unchanged"}`)
					in.ExpectedUpdatedAt = version.In(time.FixedZone("offset", 3600))
					row, e = f.repo.UpdateEndpointConfiguration(t.Context(), f.actor, family, row.ID, in)
					if e != nil {
						t.Fatal(e)
					}
					raw2, cipher2, mapping2, version2 := f.stored(t, family, row.ID)
					if raw2 != raw || !bytes.Equal(cipher2, cipher) || !bytes.Equal(mapping2, mapping) || !version2.After(version) {
						t.Fatal("display edit lost native config")
					}
					in.Secret = "rotated-secret"
					in.URL = "http://[::1]:8080/full?category=movie"
					in.ExpectedUpdatedAt = version2
					row, e = f.repo.UpdateEndpointConfiguration(t.Context(), f.actor, family, row.ID, in)
					if e != nil {
						t.Fatal(e)
					}
					raw3, cipher3, _, _ := f.stored(t, family, row.ID)
					plain, e = f.secrets.DecryptSecret(cipher3)
					if e != nil || string(plain) != "rotated-secret" || bytes.Equal(cipher3, cipher2) || raw3 != in.URL {
						t.Fatal("credential rotation failed")
					}
				})
			}
		})
	}
}

func TestEndpointConfigurationEmptySecretAndJSONPG(t *testing.T) {
	f := newEndpointConfigFixture(t)
	for _, family := range []ports.EndpointFamily{ports.DownloadClientEndpoint, ports.MediaServerEndpoint} {
		t.Run(string(family), func(t *testing.T) {
			in := endpointConfigInput(family)
			in.Secret = ""
			in.Mapping = nil
			row, e := f.repo.CreateEndpointConfiguration(t.Context(), f.actor, family, in)
			if e != nil {
				t.Fatal(e)
			}
			_, cipher, mapping, _ := f.stored(t, family, row.ID)
			plain, e := f.secrets.DecryptSecret(cipher)
			if e != nil || len(cipher) == 0 || len(plain) != 0 || string(mapping) != "{}" {
				t.Fatal("empty secret was not encrypted or mapping default missing")
			}
		})
	}
	encoded, e := json.Marshal(ports.EndpointConfigurationInput{URL: "private-url", Secret: "native-secret", Mapping: json.RawMessage(`{"private":"data"}`)})
	if e != nil || strings.Contains(string(encoded), "private") || strings.Contains(string(encoded), "native-secret") {
		t.Fatal("transient fields serialized")
	}
	scope, e := json.Marshal(ports.EndpointProbeScope{ConfigStamp: "opaque-secret-stamp"})
	if e != nil || strings.Contains(string(scope), "opaque-secret-stamp") {
		t.Fatal("probe stamp serialized")
	}
}

func TestEndpointConfigurationVersionAndLossyURLPG(t *testing.T) {
	f := newEndpointConfigFixture(t)
	for _, family := range []ports.EndpointFamily{ports.DownloadClientEndpoint, ports.MediaServerEndpoint} {
		t.Run(string(family), func(t *testing.T) {
			row := f.create(t, family)
			in := endpointConfigInput(family)
			in.Secret = ""
			for _, raw := range []string{row.URL, "https://replacement.test/full?x=1"} {
				in.URL = raw
				in.ExpectedUpdatedAt = time.Time{}
				if _, e := f.repo.UpdateEndpointConfiguration(t.Context(), f.actor, family, row.ID, in); e != ports.ErrEndpointVersionConflict {
					t.Fatal("missing version accepted", e)
				}
				in.ExpectedUpdatedAt = row.UpdatedAt.Add(-time.Microsecond)
				if _, e := f.repo.UpdateEndpointConfiguration(t.Context(), f.actor, family, row.ID, in); e != ports.ErrEndpointVersionConflict {
					t.Fatal("stale version accepted", e)
				}
			}
			in.ExpectedUpdatedAt = row.UpdatedAt
			in.URL = "http://replacement.test/?token=%2A%2A%2A"
			if _, e := f.repo.UpdateEndpointConfiguration(t.Context(), f.actor, family, row.ID, in); e != ports.ErrInvalidEndpointConfiguration {
				t.Fatal("replacement mask accepted", e)
			}
			table := "download_client"
			if family == ports.MediaServerEndpoint {
				table = "media_server"
			}
			for _, raw := range []string{"http://user:private@localhost/path?page=2#private-fragment", "http://localhost/path?token=private&broken=%zz"} {
				f.exec(t, "UPDATE "+table+" SET url=$2 WHERE id=$1", row.ID, raw)
				in.URL = endpoint.DisplayURL(raw)
				in.ExpectedUpdatedAt = row.UpdatedAt
				updated, e := f.repo.UpdateEndpointConfiguration(t.Context(), f.actor, family, row.ID, in)
				if e != nil {
					t.Fatal("lossy legacy projection rejected", e)
				}
				stored, _, _, _ := f.stored(t, family, row.ID)
				if stored != raw {
					t.Fatal("lossy URL was overwritten")
				}
				row = updated
			}
		})
	}
}

func TestEndpointConfigurationValidationPG(t *testing.T) {
	f := newEndpointConfigFixture(t)
	cases := []struct {
		name string
		edit func(*ports.EndpointConfigurationInput)
	}{
		{"blank_name", func(i *ports.EndpointConfigurationInput) { i.Name = " " }},
		{"name_length", func(i *ports.EndpointConfigurationInput) { i.Name = strings.Repeat("x", 257) }},
		{"name_control", func(i *ports.EndpointConfigurationInput) { i.Name = "bad\nname" }},
		{"protocol", func(i *ports.EndpointConfigurationInput) { i.Protocol = "unknown" }},
		{"ftp", func(i *ports.EndpointConfigurationInput) { i.URL = "ftp://localhost/path" }},
		{"relative", func(i *ports.EndpointConfigurationInput) { i.URL = "/relative" }},
		{"host", func(i *ports.EndpointConfigurationInput) { i.URL = "http:///path" }},
		{"port", func(i *ports.EndpointConfigurationInput) { i.URL = "http://localhost:99999" }},
		{"mask", func(i *ports.EndpointConfigurationInput) { i.URL = "http://localhost/?token=***" }},
		{"escaped_mask", func(i *ports.EndpointConfigurationInput) { i.URL = "http://localhost/?token=%2A%2A%2A" }},
		{"bad_escape", func(i *ports.EndpointConfigurationInput) { i.URL = "http://localhost/?x=%zz" }},
		{"username", func(i *ports.EndpointConfigurationInput) { i.Username = "bad\x00user" }},
		{"secret_length", func(i *ports.EndpointConfigurationInput) { i.Secret = strings.Repeat("x", 65537) }},
		{"mapping_array", func(i *ports.EndpointConfigurationInput) { i.Mapping = json.RawMessage(`[]`) }},
		{"mapping_null", func(i *ports.EndpointConfigurationInput) { i.Mapping = json.RawMessage(`null`) }},
		{"mapping_value_null", func(i *ports.EndpointConfigurationInput) { i.Mapping = json.RawMessage(`{"x":null}`) }},
		{"mapping_number", func(i *ports.EndpointConfigurationInput) { i.Mapping = json.RawMessage(`{"x":12}`) }},
		{"mapping_path", func(i *ports.EndpointConfigurationInput) { i.Mapping = json.RawMessage(`{"x":"bad\u0000path"}`) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := endpointConfigInput(ports.DownloadClientEndpoint)
			tc.edit(&in)
			if _, e := f.repo.CreateEndpointConfiguration(t.Context(), f.actor, ports.DownloadClientEndpoint, in); e != ports.ErrInvalidEndpointConfiguration {
				t.Fatal("invalid input accepted", e)
			}
		})
	}
	if _, e := f.repo.CreateEndpointConfiguration(t.Context(), f.actor, "unknown", endpointConfigInput(ports.DownloadClientEndpoint)); e != ports.ErrInvalidEndpointConfiguration {
		t.Fatal("invalid family accepted", e)
	}
	a, b := f.counts(t)
	if a != 0 || b != 0 {
		t.Fatal("validation wrote success records")
	}
}

func TestEndpointConfigurationActorsAndListFailurePG(t *testing.T) {
	f := newEndpointConfigFixture(t)
	member := pgtest.CreateUserActor(t, db.New(f.p), &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember})
	for _, a := range []*ports.Actor{f.actor, member} {
		if list, e := f.repo.ListEndpointConfigurations(t.Context(), a, ports.DownloadClientEndpoint); e != nil || list == nil {
			t.Fatal("current member/admin cannot list", e)
		}
	}
	for _, a := range []*ports.Actor{nil, member, {Type: ports.ActorTypeUser, ID: f.actor.ID, Role: ports.RoleAdmin, Scopes: []string{}}, {Type: ports.ActorTypeUser, ID: f.actor.ID, Role: ports.RoleAdmin, Scopes: []string{"admin", "write"}}, {Type: ports.ActorTypeSystem, ID: f.actor.ID, Role: ports.RoleAdmin}} {
		if _, e := f.repo.CreateEndpointConfiguration(t.Context(), a, ports.DownloadClientEndpoint, endpointConfigInput(ports.DownloadClientEndpoint)); e != ports.ErrForbidden && e != ports.ErrUnauthorized {
			t.Fatal("actor escalated", e)
		}
	}
	claimed := *member
	claimed.Role = ports.RoleAdmin
	if _, e := f.repo.ListEndpointConfigurations(t.Context(), &claimed, ports.DownloadClientEndpoint); e != ports.ErrForbidden {
		t.Fatal("claimed role accepted", e)
	}
	readToken := *member
	readToken.Scopes = []string{"read"}
	if _, e := f.repo.ListEndpointConfigurations(t.Context(), &readToken, ports.DownloadClientEndpoint); e != nil {
		t.Fatal(e)
	}
	f.exec(t, "UPDATE app_user SET is_active=false WHERE id=$1", member.ID)
	if _, e := f.repo.ListEndpointConfigurations(t.Context(), member, ports.DownloadClientEndpoint); e != ports.ErrForbidden {
		t.Fatal("inactive user accepted", e)
	}
	f.p.Close()
	for _, family := range []ports.EndpointFamily{ports.DownloadClientEndpoint, ports.MediaServerEndpoint} {
		if _, e := f.repo.ListEndpointConfigurations(t.Context(), f.actor, family); e != ports.ErrUnavailable {
			t.Fatal("list outage became success", e)
		}
	}
}

func TestEndpointConfigurationConcurrentEditPG(t *testing.T) {
	f := newEndpointConfigFixture(t)
	for _, family := range []ports.EndpointFamily{ports.DownloadClientEndpoint, ports.MediaServerEndpoint} {
		t.Run(string(family), func(t *testing.T) {
			row := f.create(t, family)
			in := endpointConfigInput(family)
			in.URL = "http://localhost/complete"
			in.ExpectedUpdatedAt = row.UpdatedAt
			start := make(chan struct{})
			done := make(chan error, 2)
			var wg sync.WaitGroup
			for range 2 {
				wg.Go(func() {
					<-start
					_, e := f.repo.UpdateEndpointConfiguration(t.Context(), f.actor, family, row.ID, in)
					done <- e
				})
			}
			close(start)
			wg.Wait()
			close(done)
			ok, conflict := 0, 0
			for e := range done {
				switch e {
				case nil:
					ok++
				case ports.ErrEndpointVersionConflict:
					conflict++
				default:
					t.Fatal(e)
				}
			}
			if ok != 1 || conflict != 1 {
				t.Fatal("concurrent overwrite", ok, conflict)
			}
		})
	}
}

func TestEndpointConfigurationProbeScopePG(t *testing.T) {
	f := newEndpointConfigFixture(t)
	for _, family := range []ports.EndpointFamily{ports.DownloadClientEndpoint, ports.MediaServerEndpoint} {
		t.Run(string(family), func(t *testing.T) {
			row := f.create(t, family)
			scope, e := f.repo.AuthorizeEndpointProbe(t.Context(), f.actor, family, row.ID)
			if e != nil {
				t.Fatal(e)
			}
			a, b := f.counts(t)
			if e = f.repo.CompleteEndpointProbe(t.Context(), f.actor, *scope, false); e != nil {
				t.Fatal(e)
			}
			_, _, _, version := f.stored(t, family, row.ID)
			if !version.Equal(row.UpdatedAt) {
				t.Fatal("probe bumped config version")
			}
			a2, b2 := f.counts(t)
			if a2 != a+1 || b2 != b+1 {
				t.Fatal("probe audit/outbox missing")
			}
			table, protocol, cipher, mapping := "download_client", "client_type", "password_encrypted", "remote_path_map"
			otherProtocol := "transmission"
			if family == ports.MediaServerEndpoint {
				table, protocol, cipher, mapping = "media_server", "server_type", "api_key_encrypted", "path_mapping"
				otherProtocol = "plex"
			}
			changes := []struct {
				column string
				value  any
			}{{"url", "http://localhost/changed?token=other"}, {protocol, otherProtocol}, {cipher, []byte("new-encrypted-bytes")}, {mapping, `{"/other":"/path"}`}, {"name", "Changed endpoint"}}
			if family == ports.DownloadClientEndpoint {
				changes = append(changes, struct {
					column string
					value  any
				}{"username", "changed-user"})
			}
			for _, change := range changes {
				t.Run(change.column, func(t *testing.T) {
					scope, e := f.repo.AuthorizeEndpointProbe(t.Context(), f.actor, family, row.ID)
					if e != nil {
						t.Fatal(e)
					}
					f.exec(t, "UPDATE "+table+" SET "+change.column+"=$2 WHERE id=$1", row.ID, change.value)
					if e = f.repo.CompleteEndpointProbe(t.Context(), f.actor, *scope, true); e != ports.ErrEndpointVersionConflict {
						t.Fatal("same-version config change accepted", e)
					}
				})
			}
			scope, e = f.repo.AuthorizeEndpointProbe(t.Context(), f.actor, family, row.ID)
			if e != nil {
				t.Fatal(e)
			}
			f.exec(t, "UPDATE "+table+" SET updated_at=updated_at+interval '1 microsecond' WHERE id=$1", row.ID)
			if e = f.repo.CompleteEndpointProbe(t.Context(), f.actor, *scope, true); e != ports.ErrEndpointVersionConflict {
				t.Fatal("stale version accepted", e)
			}
			scope, e = f.repo.AuthorizeEndpointProbe(t.Context(), f.actor, family, row.ID)
			if e != nil {
				t.Fatal(e)
			}
			scope.ConfigStamp = "forged"
			if e = f.repo.CompleteEndpointProbe(t.Context(), f.actor, *scope, true); e != ports.ErrEndpointVersionConflict {
				t.Fatal("forged stamp accepted", e)
			}
		})
	}
}

func TestEndpointConfigurationAccountLockRevocationPG(t *testing.T) {
	f := newEndpointConfigFixture(t)
	for _, family := range []ports.EndpointFamily{ports.DownloadClientEndpoint, ports.MediaServerEndpoint} {
		for _, operation := range []string{"create", "update", "delete", "authorize", "complete"} {
			for _, revoke := range []string{"disable", "downgrade"} {
				t.Run(string(family)+"/"+operation+"/"+revoke, func(t *testing.T) {
					f.exec(t, "UPDATE app_user SET is_active=true,role='admin' WHERE id=$1", f.actor.ID)
					row := f.create(t, family)
					scope, e := f.repo.AuthorizeEndpointProbe(t.Context(), f.actor, family, row.ID)
					if e != nil {
						t.Fatal(e)
					}
					beforeA, beforeB := f.counts(t)
					ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
					defer cancel()
					tx, e := f.p.Begin(ctx)
					if e != nil {
						t.Fatal(e)
					}
					stopped := make(chan struct{})
					done := make(chan error, 1)
					defer func() { cancel(); <-stopped }()
					defer func() { _ = tx.Rollback(context.Background()) }()
					if _, e = tx.Exec(ctx, "SELECT id FROM app_user WHERE id=$1 FOR UPDATE", f.actor.ID); e != nil {
						close(stopped)
						t.Fatal(e)
					}
					go func() {
						defer close(stopped)
						var err error
						switch operation {
						case "create":
							_, err = f.repo.CreateEndpointConfiguration(ctx, f.actor, family, endpointConfigInput(family))
						case "update":
							in := endpointConfigInput(family)
							in.ExpectedUpdatedAt = row.UpdatedAt
							_, err = f.repo.UpdateEndpointConfiguration(ctx, f.actor, family, row.ID, in)
						case "delete":
							err = f.repo.DeleteEndpointConfiguration(ctx, f.actor, family, row.ID)
						case "authorize":
							_, err = f.repo.AuthorizeEndpointProbe(ctx, f.actor, family, row.ID)
						case "complete":
							err = f.repo.CompleteEndpointProbe(ctx, f.actor, *scope, true)
						}
						done <- err
					}()
					ticker := time.NewTicker(5 * time.Millisecond)
					defer ticker.Stop()
					for {
						var blocked bool
						if e = f.p.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1::integer=ANY(pg_blocking_pids(pid)))`, tx.Conn().PgConn().PID()).Scan(&blocked); e != nil {
							t.Fatal("account wait not observed", e)
						}
						if blocked {
							break
						}
						select {
						case <-ctx.Done():
							t.Fatal("no lock wait")
						case <-ticker.C:
						}
					}
					sql := "UPDATE app_user SET is_active=false WHERE id=$1"
					if revoke == "downgrade" {
						sql = "UPDATE app_user SET role='member' WHERE id=$1"
					}
					if _, e = tx.Exec(ctx, sql, f.actor.ID); e != nil {
						t.Fatal(e)
					}
					if e = tx.Commit(ctx); e != nil {
						t.Fatal(e)
					}
					if e = <-done; e != ports.ErrForbidden {
						t.Fatal("post-wait revocation bypassed", e)
					}
					a, b := f.counts(t)
					if a != beforeA || b != beforeB {
						t.Fatal("revoked operation wrote success")
					}
					_, _, _, version := f.stored(t, family, row.ID)
					if !version.Equal(row.UpdatedAt) {
						t.Fatal("revoked operation changed endpoint")
					}
				})
			}
		}
	}
}

func TestEndpointConfigurationLastWriteRollbackPG(t *testing.T) {
	f := newEndpointConfigFixture(t)
	for _, family := range []ports.EndpointFamily{ports.DownloadClientEndpoint, ports.MediaServerEndpoint} {
		for _, fault := range []string{"audit_log", "outbox_event"} {
			for _, operation := range []string{"create", "update", "delete", "complete"} {
				t.Run(string(family)+"/"+fault+"/"+operation, func(t *testing.T) {
					row := f.create(t, family)
					scope, e := f.repo.AuthorizeEndpointProbe(t.Context(), f.actor, family, row.ID)
					if e != nil {
						t.Fatal(e)
					}
					raw, cipher, mapping, version := f.stored(t, family, row.ID)
					a, b := f.counts(t)
					before, e := f.repo.ListEndpointConfigurations(t.Context(), f.actor, family)
					if e != nil {
						t.Fatal(e)
					}
					f.exec(t, `CREATE OR REPLACE FUNCTION endpoint_config_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'private-url native-secret driver-error'; END $$`)
					f.exec(t, "CREATE TRIGGER endpoint_config_fault BEFORE INSERT ON "+fault+" FOR EACH ROW EXECUTE FUNCTION endpoint_config_fault()")
					defer f.exec(t, "DROP TRIGGER endpoint_config_fault ON "+fault)
					switch operation {
					case "create":
						_, e = f.repo.CreateEndpointConfiguration(t.Context(), f.actor, family, endpointConfigInput(family))
					case "update":
						in := endpointConfigInput(family)
						in.Secret = "rotation"
						in.ExpectedUpdatedAt = version
						_, e = f.repo.UpdateEndpointConfiguration(t.Context(), f.actor, family, row.ID, in)
					case "delete":
						e = f.repo.DeleteEndpointConfiguration(t.Context(), f.actor, family, row.ID)
					case "complete":
						e = f.repo.CompleteEndpointProbe(t.Context(), f.actor, *scope, true)
					}
					if e != ports.ErrUnavailable {
						t.Fatal("unsafe or missing rollback error", e)
					}
					raw2, cipher2, mapping2, version2 := f.stored(t, family, row.ID)
					a2, b2 := f.counts(t)
					after, e := f.repo.ListEndpointConfigurations(t.Context(), f.actor, family)
					if e != nil || len(before) != len(after) {
						t.Fatal("endpoint count changed after rollback", e)
					}
					if raw2 != raw || !bytes.Equal(cipher2, cipher) || !bytes.Equal(mapping2, mapping) || !version2.Equal(version) || a2 != a || b2 != b {
						t.Fatal("partial configuration transaction committed")
					}
				})
			}
		}
	}
}

func TestEndpointConfigurationDeletionReferencesPG(t *testing.T) {
	f := newEndpointConfigFixture(t)
	for _, kind := range []string{"download_submission", "episode_download_submission", "complete_tv_coverage_download_submission", "automatic_tv_download_submission", "movie_upgrade_native_send"} {
		for _, state := range []string{"queued", "running"} {
			t.Run(kind+"/"+state, func(t *testing.T) {
				row := f.create(t, ports.DownloadClientEndpoint)
				payload, _ := json.Marshal(map[string]any{"snapshot": map[string]any{"target": map[string]any{"client_id": row.ID}}})
				job := uuid.New()
				f.exec(t, "INSERT INTO job(id,kind,state,payload)VALUES($1,$2,$3,$4)", job, kind, state, payload)
				if e := f.repo.DeleteEndpointConfiguration(t.Context(), f.actor, ports.DownloadClientEndpoint, row.ID); e != ports.ErrEndpointInUse {
					t.Fatal("queued execution orphaned", e)
				}
				f.exec(t, "UPDATE job SET state='succeeded' WHERE id=$1", job)
				if e := f.repo.DeleteEndpointConfiguration(t.Context(), f.actor, ports.DownloadClientEndpoint, row.ID); e != nil {
					t.Fatal("unused endpoint not deleted", e)
				}
				if e := f.repo.DeleteEndpointConfiguration(t.Context(), f.actor, ports.DownloadClientEndpoint, row.ID); e != ports.ErrNotFound {
					t.Fatal("missing endpoint faked success", e)
				}
			})
		}
	}
	// Exercise real FK errors for both families without disabling any production guard.
	for _, family := range []ports.EndpointFamily{ports.DownloadClientEndpoint, ports.MediaServerEndpoint} {
		t.Run(string(family)+"/foreign_key", func(t *testing.T) {
			table := "download_client"
			if family == ports.MediaServerEndpoint {
				table = "media_server"
			}
			row := f.create(t, family)
			f.exec(t, "CREATE TABLE retained_endpoint_reference_"+table+"(endpoint_id uuid REFERENCES "+table+"(id) ON DELETE RESTRICT)")
			f.exec(t, "INSERT INTO retained_endpoint_reference_"+table+" VALUES($1)", row.ID)
			if e := f.repo.DeleteEndpointConfiguration(t.Context(), f.actor, family, row.ID); e != ports.ErrEndpointInUse {
				t.Fatal("FK not mapped to in-use", e)
			}
			f.exec(t, "DELETE FROM retained_endpoint_reference_"+table)
			if e := f.repo.DeleteEndpointConfiguration(t.Context(), f.actor, family, row.ID); e != nil {
				t.Fatal(e)
			}
			if e := f.repo.DeleteEndpointConfiguration(t.Context(), f.actor, family, row.ID); e != ports.ErrNotFound {
				t.Fatal("missing endpoint faked success", e)
			}
		})
	}
}

func TestEndpointConfigurationSafeSuccessPayloadPG(t *testing.T) {
	f := newEndpointConfigFixture(t)
	for _, family := range []ports.EndpointFamily{ports.DownloadClientEndpoint, ports.MediaServerEndpoint} {
		row := f.create(t, family)
		scope, e := f.repo.AuthorizeEndpointProbe(t.Context(), f.actor, family, row.ID)
		if e != nil {
			t.Fatal(e)
		}
		if e = f.repo.CompleteEndpointProbe(t.Context(), f.actor, *scope, true); e != nil {
			t.Fatal(e)
		}
	}
	rows, e := f.p.Query(t.Context(), `SELECT payload FROM audit_log UNION ALL SELECT payload FROM outbox_event`)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if e = rows.Scan(&raw); e != nil {
			t.Fatal(e)
		}
		var payload map[string]any
		if e = json.Unmarshal(raw, &payload); e != nil {
			t.Fatal(e)
		}
		for key := range payload {
			if key != "id" && key != "family" && key != "version" && key != "alive" {
				t.Fatal("unsafe payload field", key)
			}
		}
		if strings.Contains(string(raw), "private") || strings.Contains(string(raw), "native-") {
			t.Fatal("sensitive payload")
		}
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
}

func TestEndpointConfigurationCancellationAndMissingPG(t *testing.T) {
	f := newEndpointConfigFixture(t)
	family := ports.DownloadClientEndpoint
	row := f.create(t, family)
	scope, e := f.repo.AuthorizeEndpointProbe(t.Context(), f.actor, family, row.ID)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	checks := []func() error{
		func() error { _, e := f.repo.ListEndpointConfigurations(ctx, f.actor, family); return e },
		func() error {
			_, e := f.repo.CreateEndpointConfiguration(ctx, f.actor, family, endpointConfigInput(family))
			return e
		},
		func() error {
			_, e := f.repo.UpdateEndpointConfiguration(ctx, f.actor, family, row.ID, endpointConfigInput(family))
			return e
		},
		func() error { return f.repo.DeleteEndpointConfiguration(ctx, f.actor, family, row.ID) },
		func() error { _, e := f.repo.AuthorizeEndpointProbe(ctx, f.actor, family, row.ID); return e },
		func() error { return f.repo.CompleteEndpointProbe(ctx, f.actor, *scope, true) },
	}
	for n, check := range checks {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			if e := check(); !errors.Is(e, context.Canceled) {
				t.Fatal("cancellation lost", e)
			}
		})
	}
	id := uuid.New()
	if _, e = f.repo.UpdateEndpointConfiguration(t.Context(), f.actor, family, id, endpointConfigInput(family)); e != ports.ErrNotFound {
		t.Fatal(e)
	}
	if _, e = f.repo.AuthorizeEndpointProbe(t.Context(), f.actor, family, id); e != ports.ErrNotFound {
		t.Fatal(e)
	}
	missing := *scope
	missing.ID = id
	if e = f.repo.CompleteEndpointProbe(t.Context(), f.actor, missing, true); e != ports.ErrNotFound {
		t.Fatal(e)
	}
}

func TestEndpointConfigurationProtectorFailuresPG(t *testing.T) {
	f := newEndpointConfigFixture(t)
	bad := postgres.NewEndpointConfigurationStore(f.p, idgen.New(), crypto.NewSecretProtector([]byte("bad-key")))
	for _, family := range []ports.EndpointFamily{ports.DownloadClientEndpoint, ports.MediaServerEndpoint} {
		t.Run(string(family), func(t *testing.T) {
			row := f.create(t, family)
			a, b := f.counts(t)
			raw, cipher, mapping, version := f.stored(t, family, row.ID)
			if _, e := bad.CreateEndpointConfiguration(t.Context(), f.actor, family, endpointConfigInput(family)); e != ports.ErrUnavailable {
				t.Fatal("encryption failure exposed", e)
			}
			in := endpointConfigInput(family)
			in.ExpectedUpdatedAt = version
			if _, e := bad.UpdateEndpointConfiguration(t.Context(), f.actor, family, row.ID, in); e != ports.ErrUnavailable {
				t.Fatal("rotation failure exposed", e)
			}
			if _, e := bad.AuthorizeEndpointProbe(t.Context(), f.actor, family, row.ID); e != ports.ErrUnavailable {
				t.Fatal("digest failure exposed", e)
			}
			scope, e := f.repo.AuthorizeEndpointProbe(t.Context(), f.actor, family, row.ID)
			if e != nil {
				t.Fatal(e)
			}
			if e = bad.CompleteEndpointProbe(t.Context(), f.actor, *scope, true); e != ports.ErrUnavailable {
				t.Fatal("completion digest failure exposed", e)
			}
			raw2, cipher2, mapping2, version2 := f.stored(t, family, row.ID)
			a2, b2 := f.counts(t)
			if raw2 != raw || !bytes.Equal(cipher2, cipher) || !bytes.Equal(mapping2, mapping) || !version2.Equal(version) || a2 != a || b2 != b {
				t.Fatal("protector failure changed config")
			}
		})
	}
}

func TestEndpointConfigurationMemberAndTokenCannotMutatePG(t *testing.T) {
	f := newEndpointConfigFixture(t)
	member := pgtest.CreateUserActor(t, db.New(f.p), &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember})
	scoped := *f.actor
	scoped.Scopes = []string{"read", "write", "admin"}
	forged := *member
	forged.Role = ports.RoleAdmin
	for _, family := range []ports.EndpointFamily{ports.DownloadClientEndpoint, ports.MediaServerEndpoint} {
		row := f.create(t, family)
		scope, e := f.repo.AuthorizeEndpointProbe(t.Context(), f.actor, family, row.ID)
		if e != nil {
			t.Fatal(e)
		}
		for n, a := range []*ports.Actor{member, &scoped, &forged} {
			t.Run(fmt.Sprintf("%s/%d", family, n), func(t *testing.T) {
				in := endpointConfigInput(family)
				in.ExpectedUpdatedAt = row.UpdatedAt
				if _, e := f.repo.UpdateEndpointConfiguration(t.Context(), a, family, row.ID, in); e != ports.ErrForbidden {
					t.Fatal("unauthorized update", e)
				}
				if e := f.repo.DeleteEndpointConfiguration(t.Context(), a, family, row.ID); e != ports.ErrForbidden {
					t.Fatal("unauthorized delete", e)
				}
				if _, e := f.repo.AuthorizeEndpointProbe(t.Context(), a, family, row.ID); e != ports.ErrForbidden {
					t.Fatal("unauthorized probe", e)
				}
				if e := f.repo.CompleteEndpointProbe(t.Context(), a, *scope, true); e != ports.ErrForbidden {
					t.Fatal("unauthorized completion", e)
				}
			})
		}
	}
}

func TestEndpointConfigurationRetainedDatabaseReferencesPG(t *testing.T) {
	f := newEndpointConfigFixture(t)
	t.Run("archived_subscription", func(t *testing.T) {
		row := f.create(t, ports.DownloadClientEndpoint)
		media, root, sub := uuid.New(), uuid.New(), uuid.New()
		f.exec(t, "INSERT INTO media(id,title,media_type)VALUES($1,'Reference movie','movie')", media)
		f.exec(t, "INSERT INTO library_root(id,path,media_type,name)VALUES($1,$2,'movie','Reference root')", root, t.TempDir())
		f.exec(t, "INSERT INTO subscription(id,user_id,media_id,root_id,downloader_id,state)VALUES($1,$2,$3,$4,$5,'archived')", sub, f.actor.ID, media, root, row.ID)
		if e := f.repo.DeleteEndpointConfiguration(t.Context(), f.actor, ports.DownloadClientEndpoint, row.ID); e != ports.ErrEndpointInUse {
			t.Fatal("subscription binding discarded", e)
		}
		var bound uuid.UUID
		if e := f.p.QueryRow(t.Context(), "SELECT downloader_id FROM subscription WHERE id=$1", sub).Scan(&bound); e != nil || bound != row.ID {
			t.Fatal("subscription binding changed", e)
		}
	})
	t.Run("completed_download_task", func(t *testing.T) {
		row := f.create(t, ports.DownloadClientEndpoint)
		indexer, candidate, task := uuid.New(), uuid.New(), uuid.New()
		cipher, e := f.secrets.EncryptSecret([]byte("history"))
		if e != nil {
			t.Fatal(e)
		}
		f.exec(t, "INSERT INTO indexer(id,name,url,api_key_encrypted)VALUES($1,'History source','http://localhost/',$2)", indexer, cipher)
		f.exec(t, "INSERT INTO release_candidate(id,indexer_id,title,guid,link_encrypted,size_bytes)VALUES($1,$2,'History title','history-guid',$3,123)", candidate, indexer, cipher)
		f.exec(t, "INSERT INTO download_task(id,operation_id,client_id,candidate_id,info_hash,state,save_path)VALUES($1,$2,$3,$4,$5,'completed',$6)", task, uuid.New(), row.ID, candidate, strings.Repeat("a", 40), t.TempDir())
		if e := f.repo.DeleteEndpointConfiguration(t.Context(), f.actor, ports.DownloadClientEndpoint, row.ID); e != ports.ErrEndpointInUse {
			t.Fatal("completed task history discarded", e)
		}
		var bound uuid.UUID
		if e := f.p.QueryRow(t.Context(), "SELECT client_id FROM download_task WHERE id=$1", task).Scan(&bound); e != nil || bound != row.ID {
			t.Fatal("task history changed", e)
		}
	})
	// The immutable execution tables keep their real restrictive FKs; no private
	// delete path may turn these histories into cascade cleanup.
	for _, table := range []string{"episode_execution_authority", "complete_tv_execution_authority", "automatic_tv_execution_authority", "movie_upgrade_authority"} {
		t.Run(table, func(t *testing.T) {
			var restrictive bool
			e := f.p.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_constraint WHERE contype='f' AND conrelid=$1::regclass AND confrelid='download_client'::regclass AND confdeltype IN ('a','r'))`, table).Scan(&restrictive)
			if e != nil || !restrictive {
				t.Fatal("frozen endpoint history is not FK protected", e)
			}
		})
	}
}
