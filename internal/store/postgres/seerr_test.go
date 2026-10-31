package postgres_test

import (
	"bytes"
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	seerr "github.com/dalamudx/PulseMedia/internal/usecase/seerr"
	"github.com/google/uuid"
	"strings"
	"sync"
	"testing"
)

func seerrFixture(t *testing.T) (mediaRequestFixture, *postgres.SeerrStore, *seerr.Service, string) {
	t.Helper()
	f := newMediaRequestFixture(t)
	if v, e := postgres.Migrate(t.Context(), f.dsn, "../../../migrations"); e != nil || v != postgres.CurrentMigrationVersion {
		t.Fatal("latest schema", v, e)
	}
	s := postgres.NewSeerrStore(f.pool, idgen.New(), clock.NewMock(f.now), crypto.NewSecretProtector(bytes.Repeat([]byte{31}, 32)))
	secret := strings.Repeat("dedicated-seerr-credential-", 2)
	return f, s, seerr.NewService(s), secret
}
func seerrPayload(id string) []byte {
	return []byte(`{"notification_type":"MEDIA_APPROVED","media":{"media_type":"movie","tmdbId":"550"},"request":{"request_id":"` + id + `","requestedBy_email":"never-persist@example.invalid"}}`)
}
func TestSeerrIngressDedupOwnerAndPermissionPG(t *testing.T) {
	f, _, svc, secret := seerrFixture(t)
	ctx := t.Context()
	empty, e := svc.Configuration(ctx, f.admin)
	if e != nil || empty.Enabled || empty.Version != 0 {
		t.Fatal("must default disabled", empty, e)
	}
	if _, e = svc.Receive(ctx, secret, seerrPayload("72")); !errors.Is(e, ports.ErrUnauthorized) {
		t.Fatal("unconfigured ingress", e)
	}
	cfg, e := svc.Save(ctx, f.admin, ports.SeerrConfigurationInput{OwnerID: uuid.MustParse(f.owner.ID), Enabled: true, Credential: secret})
	if e != nil || cfg.Version != 1 || !cfg.CredentialPresent {
		t.Fatal("configuration", cfg, e)
	}
	var cipher []byte
	if e = f.pool.QueryRow(ctx, `SELECT credential_encrypted FROM seerr_configuration`).Scan(&cipher); e != nil || bytes.Contains(cipher, []byte(secret)) {
		t.Fatal("credential plaintext", e)
	}
	var wg sync.WaitGroup
	results := make([]ports.SeerrReceipt, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(i int) { defer wg.Done(); results[i], errs[i] = svc.Receive(ctx, secret, seerrPayload("72")) }(i)
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || results[0] != results[1] {
		t.Fatal("dedup", errs)
	}
	row, e := f.store.ReadMediaRequest(ctx, f.owner, results[0].RequestID)
	if e != nil || row.State != "pending" || row.Version != 1 || row.OwnerID.String() != f.owner.ID {
		t.Fatal("pending local request", row, e)
	}
	if n := f.count(t, `SELECT count(*) FROM subscription`); n != 0 {
		t.Fatal("ingress bypassed approval")
	}
	if n := f.count(t, `SELECT count(*) FROM media_request`); n != 1 {
		t.Fatal("duplicate local request", n)
	}
	changed := bytes.Replace(seerrPayload("72"), []byte("550"), []byte("551"), 1)
	if _, e = svc.Receive(ctx, secret, changed); !errors.Is(e, ports.ErrMediaRequestConflict) {
		t.Fatal("changed payload accepted", e)
	}
	_, e = svc.Save(ctx, f.admin, ports.SeerrConfigurationInput{OwnerID: uuid.MustParse(f.other.ID), ExpectedVersion: 1, Enabled: true})
	if !errors.Is(e, ports.ErrMediaRequestConflict) {
		t.Fatal("owner rebound", e)
	}
	_, e = svc.Save(ctx, f.admin, ports.SeerrConfigurationInput{OwnerID: uuid.MustParse(f.owner.ID), ExpectedVersion: 1, Enabled: false})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = svc.Receive(ctx, secret, seerrPayload("73")); !errors.Is(e, ports.ErrUnauthorized) {
		t.Fatal("disabled ingress", e)
	}
	_, e = svc.Save(ctx, f.admin, ports.SeerrConfigurationInput{OwnerID: uuid.MustParse(f.owner.ID), ExpectedVersion: 2, Enabled: true})
	if e != nil {
		t.Fatal(e)
	}
	again, e := svc.Receive(ctx, secret, seerrPayload("72"))
	if e != nil || again != results[0] {
		t.Fatal("version rotation recreated receipt", e)
	}
	f.exec(t, `UPDATE app_user SET is_active=false WHERE id=$1`, uuid.MustParse(f.owner.ID))
	if _, e = svc.Receive(ctx, secret, seerrPayload("74")); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("disabled owner accepted", e)
	}
	f.exec(t, `UPDATE app_user SET is_active=true WHERE id=$1`, uuid.MustParse(f.owner.ID))
	f.exec(t, `UPDATE app_user SET role='member' WHERE id=$1`, uuid.MustParse(f.admin.ID))
	if _, e = svc.Receive(ctx, secret, seerrPayload("75")); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("revoked configuring admin accepted", e)
	}
	var logs string
	if e = f.pool.QueryRow(ctx, `SELECT coalesce(jsonb_agg(payload)::text,'') FROM audit_log`).Scan(&logs); e != nil || strings.Contains(logs, secret) || strings.Contains(logs, "never-persist") {
		t.Fatal("secret or inbound private fields in audit", e)
	}
}
func TestSeerrIngressAtomicRollbackAndCredentialRotationPG(t *testing.T) {
	f, _, svc, secret := seerrFixture(t)
	ctx := t.Context()
	_, e := svc.Save(ctx, f.admin, ports.SeerrConfigurationInput{OwnerID: uuid.MustParse(f.owner.ID), Enabled: true, Credential: secret})
	if e != nil {
		t.Fatal(e)
	}
	f.exec(t, `CREATE FUNCTION reject_seerr_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture refusal'; END $$`)
	f.exec(t, `CREATE TRIGGER reject_seerr_fixture BEFORE INSERT ON seerr_ingress_receipt FOR EACH ROW EXECUTE FUNCTION reject_seerr_fixture()`)
	if _, e = svc.Receive(ctx, secret, seerrPayload("81")); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal("failure not sanitized", e)
	}
	if f.count(t, `SELECT count(*) FROM media_request`) != 0 || f.count(t, `SELECT count(*) FROM idempotency_record`) != 0 || f.count(t, `SELECT count(*) FROM audit_log WHERE action='media.request.submitted'`) != 0 {
		t.Fatal("partial ingress transaction")
	}
	f.exec(t, `DROP TRIGGER reject_seerr_fixture ON seerr_ingress_receipt`)
	newSecret := strings.Repeat("rotated-credential-", 3)
	_, e = svc.Save(ctx, f.admin, ports.SeerrConfigurationInput{OwnerID: uuid.MustParse(f.owner.ID), ExpectedVersion: 1, Enabled: true, Credential: newSecret})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = svc.Receive(ctx, secret, seerrPayload("81")); !errors.Is(e, ports.ErrUnauthorized) {
		t.Fatal("old credential accepted", e)
	}
	if _, e = svc.Receive(context.Background(), newSecret, seerrPayload("81")); e != nil {
		t.Fatal(e)
	}
	token := *f.admin
	token.Scopes = []string{}
	if _, e = svc.Configuration(ctx, &token); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("Token acquired config access", e)
	}
}
