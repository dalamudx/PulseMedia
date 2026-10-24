package postgres_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
)

func rtorrentConfigFixture(t *testing.T) (endpointConfigFixture, string) {
	t.Helper()
	f := newEndpointConfigFixture(t)
	history := repositoryMigrationsThrough(t, 119)
	if version, err := postgres.Migrate(t.Context(), f.p.Config().ConnString(), history); err != nil || version != 119 {
		t.Fatal("rTorrent migration", version, err)
	}
	return f, history
}

func rtorrentConfigInput() ports.EndpointConfigurationInput {
	return ports.EndpointConfigurationInput{Name: "Owned rTorrent", Protocol: "rtorrent", URL: "https://owned-rpc.invalid/RPC2", Username: "owned-rpc-user", Secret: "owned-rpc-password", Mapping: json.RawMessage(`{"/remote":"/local"}`)}
}

func TestRTorrentConfigurationPersistencePG(t *testing.T) {
	f, _ := rtorrentConfigFixture(t)
	input := rtorrentConfigInput()
	row, err := f.repo.CreateEndpointConfiguration(t.Context(), f.actor, ports.DownloadClientEndpoint, input)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := db.New(f.p).GetDownloadClientByID(t.Context(), row.ID)
	if err != nil || stored.ClientType != "rtorrent" || bytes.Contains(stored.PasswordEncrypted, []byte(input.Secret)) {
		t.Fatal("native encrypted configuration", err)
	}
	decrypted, err := f.secrets.DecryptSecret(stored.PasswordEncrypted)
	if err != nil || string(decrypted) != input.Secret {
		t.Fatal("wrong encrypted credential")
	}
	encoded, err := json.Marshal(row)
	if err != nil || bytes.Contains(encoded, []byte(input.Secret)) {
		t.Fatal("public credential leakage")
	}
	scope, err := f.repo.AuthorizeEndpointProbe(t.Context(), f.actor, ports.DownloadClientEndpoint, row.ID)
	if err != nil {
		t.Fatal("rTorrent probe authorization", err)
	}
	audit, event := f.counts(t)
	if audit != 1 || event != 1 {
		t.Fatal("configuration and success ledger did not commit together")
	}
	input.ExpectedUpdatedAt = row.UpdatedAt
	input.Secret = "rotated-rpc-password"
	updated, err := f.repo.UpdateEndpointConfiguration(t.Context(), f.actor, ports.DownloadClientEndpoint, row.ID, input)
	if err != nil || !updated.UpdatedAt.After(row.UpdatedAt) {
		t.Fatal("versioned update", err)
	}
	if _, err := f.repo.UpdateEndpointConfiguration(t.Context(), f.actor, ports.DownloadClientEndpoint, row.ID, input); !errors.Is(err, ports.ErrEndpointVersionConflict) {
		t.Fatal("stale update accepted", err)
	}
	if err := f.repo.CompleteEndpointProbe(t.Context(), f.actor, *scope, true); !errors.Is(err, ports.ErrEndpointVersionConflict) {
		t.Fatal("late probe accepted", err)
	}
	var jobs, tasks int
	if err := f.p.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM job),(SELECT count(*) FROM download_task)`).Scan(&jobs, &tasks); err != nil || jobs != 0 || tasks != 0 {
		t.Fatal("configuration started a download", err)
	}
	f.exec(t, `UPDATE app_user SET role='member' WHERE id=$1`, f.actor.ID)
	if _, err := f.repo.ListEndpointConfigurations(t.Context(), f.actor, ports.DownloadClientEndpoint); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("live role revocation missed", err)
	}
}

func TestRTorrentConfigurationTailRollbackPG(t *testing.T) {
	for _, table := range []string{"audit_log", "outbox_event"} {
		t.Run(table, func(t *testing.T) {
			f, _ := rtorrentConfigFixture(t)
			beforeA, beforeE := f.counts(t)
			f.exec(t, `CREATE FUNCTION rtorrent_config_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'owned-rpc-password'; END $$`)
			f.exec(t, "CREATE TRIGGER rtorrent_config_fault BEFORE INSERT ON "+table+" FOR EACH ROW EXECUTE FUNCTION rtorrent_config_fault()")
			_, err := f.repo.CreateEndpointConfiguration(t.Context(), f.actor, ports.DownloadClientEndpoint, rtorrentConfigInput())
			if !errors.Is(err, ports.ErrUnavailable) || strings.Contains(err.Error(), "owned-rpc-password") {
				t.Fatal("unsafe or missing transaction failure", err)
			}
			afterA, afterE := f.counts(t)
			var clients int
			if err := f.p.QueryRow(t.Context(), `SELECT count(*) FROM download_client`).Scan(&clients); err != nil || clients != 0 || beforeA != afterA || beforeE != afterE {
				t.Fatal("partial configuration survived", err)
			}
			f.exec(t, "DROP TRIGGER rtorrent_config_fault ON "+table)
			if _, err := f.repo.CreateEndpointConfiguration(t.Context(), f.actor, ports.DownloadClientEndpoint, rtorrentConfigInput()); err != nil {
				t.Fatal("retry after rollback", err)
			}
		})
	}
}

func TestRTorrentSchema119HistoryPG(t *testing.T) {
	f, history := rtorrentConfigFixture(t)
	legacy := f.create(t, ports.DownloadClientEndpoint)
	before, err := db.New(f.p).GetDownloadClientByID(t.Context(), legacy.ID)
	if err != nil {
		t.Fatal(err)
	}
	native, err := f.repo.CreateEndpointConfiguration(t.Context(), f.actor, ports.DownloadClientEndpoint, rtorrentConfigInput())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := postgres.Rollback(t.Context(), f.p.Config().ConnString(), history); err == nil {
		t.Fatal("rollback allowed native client loss")
	}
	if _, err := db.New(f.p).GetDownloadClientByID(t.Context(), native.ID); err != nil {
		t.Fatal("failed rollback changed native client", err)
	}
	f.exec(t, `DELETE FROM download_client WHERE id=$1`, native.ID)
	if version, err := postgres.Rollback(t.Context(), f.p.Config().ConnString(), history); err != nil || version != 118 {
		t.Fatal("restore previous schema", version, err)
	}
	if _, err := f.repo.CreateEndpointConfiguration(t.Context(), f.actor, ports.DownloadClientEndpoint, rtorrentConfigInput()); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal("schema118 accepted native client", err)
	}
	if version, err := postgres.Migrate(t.Context(), f.p.Config().ConnString(), history); err != nil || version != 119 {
		t.Fatal("reinstall native support", version, err)
	}
	after, err := db.New(f.p).GetDownloadClientByID(t.Context(), legacy.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("historical client modified", err)
	}
	if _, err := f.repo.CreateEndpointConfiguration(t.Context(), f.actor, ports.DownloadClientEndpoint, rtorrentConfigInput()); err != nil {
		t.Fatal("native support not restored", err)
	}
}
