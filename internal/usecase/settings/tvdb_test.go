package settings_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/artwork"
	tvdbconfig "github.com/dalamudx/PulseMedia/internal/domain/tvdb"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/settings"
	"github.com/jackc/pgx/v5/pgxpool"
)

func tvdbSettingCounts(t *testing.T, pool *pgxpool.Pool) [7]int {
	t.Helper()
	var count [7]int
	if err := pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM setting WHERE key IN ('tvdb.credentials','tvdb.fallback')),(SELECT count(*) FROM idempotency_record),(SELECT count(*) FROM audit_log),(SELECT count(*) FROM outbox_event),(SELECT count(*) FROM job),(SELECT count(*) FROM download_task),(SELECT count(*) FROM transfer_plan)`).Scan(&count[0], &count[1], &count[2], &count[3], &count[4], &count[5], &count[6]); err != nil {
		t.Fatal(err)
	}
	return count
}
func TestTVDBEncryptedSettingsAndOfflineReceiptPG(t *testing.T) {
	pool, svc, q, admin, member := setupSettingsPoolTest(t)
	ctx := t.Context()
	request := settings.UpdateSettingRequest{Value: map[string]any{"api_key": "owned-tvdb-api-key", "pin": "owned-tvdb-pin"}}
	start := make(chan struct{})
	failures := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.UpdateSetting(ctx, admin, tvdbconfig.CredentialsSetting, request, "tvdb-original")
			failures <- err
		}()
	}
	close(start)
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if tvdbSettingCounts(t, pool) != ([7]int{1, 1, 1, 1, 0, 0, 0}) {
		t.Fatal("duplicate write or execution created")
	}
	row, err := q.GetSetting(ctx, tvdbconfig.CredentialsSetting)
	if err != nil || !artwork.ValidCiphertext(row.Value) || bytes.Contains(row.Value, []byte("owned-tvdb")) {
		t.Fatal("credential pair not encrypted", err)
	}
	plain, err := svc.GetSecret(ctx, tvdbconfig.CredentialsSetting)
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := tvdbconfig.DecodeCredentials([]byte(plain))
	if err != nil || credentials.APIKey != "owned-tvdb-api-key" || credentials.PIN != "owned-tvdb-pin" {
		t.Fatal("credential pair lost", err)
	}
	masked, err := svc.GetSetting(ctx, tvdbconfig.CredentialsSetting)
	if err != nil {
		t.Fatal(err)
	}
	offline := settings.NewService(postgres.NewSettingsStore(pool, idgen.New()))
	receipt, err := offline.ReadSettingReceipt(ctx, admin, tvdbconfig.CredentialsSetting, "tvdb-original")
	if err != nil || !receipt.FromCache {
		t.Fatal("offline receipt failed", err)
	}
	replay, err := offline.UpdateSetting(ctx, admin, tvdbconfig.CredentialsSetting, request, "tvdb-original")
	if err != nil || !replay.FromCache {
		t.Fatal("offline original POST failed", err)
	}
	different := settings.UpdateSettingRequest{Value: map[string]any{"api_key": "changed-key"}}
	if _, err := offline.UpdateSetting(ctx, admin, tvdbconfig.CredentialsSetting, different, "tvdb-original"); !errors.Is(err, ports.ErrCommandConflict) {
		t.Fatal("original key changed intent", err)
	}
	items, err := svc.ListSettings(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{masked, receipt, replay, items} {
		raw, e := json.Marshal(value)
		if e != nil || bytes.Contains(raw, []byte("owned-tvdb")) || bytes.Contains(raw, []byte("ciphertext")) {
			t.Fatal("public credential leak")
		}
	}
	var ledger string
	if err := pool.QueryRow(ctx, `SELECT concat((SELECT string_agg(payload::text,'') FROM audit_log),(SELECT string_agg(payload::text,'') FROM outbox_event),(SELECT string_agg(response_body::text,'') FROM idempotency_record))`).Scan(&ledger); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ledger, "owned-tvdb") || strings.Contains(ledger, "ciphertext") {
		t.Fatal("credential entered ledger")
	}
	if _, err := offline.UpdateSetting(ctx, admin, tvdbconfig.FallbackSetting, settings.UpdateSettingRequest{Value: map[string]any{"enabled": false}}, "tvdb-disable"); err != nil {
		t.Fatal("offline disable failed", err)
	}
	after, err := q.GetSetting(ctx, tvdbconfig.CredentialsSetting)
	if err != nil || !bytes.Equal(row.Value, after.Value) || row.Version != after.Version {
		t.Fatal("disable replaced original credentials", err)
	}
	if tvdbSettingCounts(t, pool) != ([7]int{2, 2, 2, 2, 0, 0, 0}) {
		t.Fatal("disable or recovery created execution")
	}
	if _, err := offline.ReadSettingReceipt(ctx, member, tvdbconfig.CredentialsSetting, "tvdb-original"); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("member received administrator receipt", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE app_user SET role='member' WHERE id=$1`, admin.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := offline.UpdateSetting(ctx, admin, tvdbconfig.CredentialsSetting, request, "tvdb-original"); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("revoked owner recovered receipt", err)
	}
}
func TestTVDBInvalidSettingsAndVersionConflictPG(t *testing.T) {
	pool, svc, _, admin, _ := setupSettingsPoolTest(t)
	for _, value := range []any{nil, true, map[string]any{}, map[string]any{"api_key": ""}, map[string]any{"api_key": "key", "pin": nil}, map[string]any{"api_key": "key", "url": "https://foreign.example"}} {
		if _, err := svc.UpdateSetting(t.Context(), admin, tvdbconfig.CredentialsSetting, settings.UpdateSettingRequest{Value: value}, "tvdb-invalid"); !errors.Is(err, ports.ErrInvalidSettingCommand) {
			t.Fatal("invalid credentials accepted", err)
		}
	}
	for _, value := range []any{nil, map[string]any{}, map[string]any{"enabled": nil}, map[string]any{"enabled": false, "pin": "secret"}} {
		if _, err := svc.UpdateSetting(t.Context(), admin, tvdbconfig.FallbackSetting, settings.UpdateSettingRequest{Value: value}, "tvdb-invalid-switch"); !errors.Is(err, ports.ErrInvalidSettingCommand) {
			t.Fatal("invalid fallback switch accepted", err)
		}
	}
	if tvdbSettingCounts(t, pool) != ([7]int{}) {
		t.Fatal("invalid settings left writes")
	}
	request := settings.UpdateSettingRequest{Value: map[string]any{"api_key": "owned-key"}}
	if _, err := svc.UpdateSetting(t.Context(), admin, tvdbconfig.CredentialsSetting, request, "tvdb-version-one"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateSetting(t.Context(), admin, tvdbconfig.CredentialsSetting, request, "tvdb-stale"); !errors.Is(err, ports.ErrSettingVersionConflict) {
		t.Fatal("stale version accepted", err)
	}
	if tvdbSettingCounts(t, pool) != ([7]int{1, 1, 1, 1, 0, 0, 0}) {
		t.Fatal("stale configuration left writes")
	}
}
func TestTVDBSettingsTailRollbackPG(t *testing.T) {
	pool, svc, _, admin, _ := setupSettingsPoolTest(t)
	if _, err := pool.Exec(t.Context(), `CREATE FUNCTION reject_tvdb_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'tvdb-tail'; END $$; CREATE TRIGGER reject_tvdb_receipt BEFORE INSERT ON idempotency_record FOR EACH ROW EXECUTE FUNCTION reject_tvdb_receipt()`); err != nil {
		t.Fatal(err)
	}
	request := settings.UpdateSettingRequest{Value: map[string]any{"api_key": "owned-key", "pin": "owned-pin"}}
	if _, err := svc.UpdateSetting(t.Context(), admin, tvdbconfig.CredentialsSetting, request, "tvdb-tail"); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal("tail failure classification", err)
	}
	if tvdbSettingCounts(t, pool) != ([7]int{}) {
		t.Fatal("tail failure left partial settings")
	}
	if _, err := pool.Exec(t.Context(), `DROP TRIGGER reject_tvdb_receipt ON idempotency_record`); err != nil {
		t.Fatal(err)
	}
	result, err := svc.UpdateSetting(t.Context(), admin, tvdbconfig.CredentialsSetting, request, "tvdb-tail")
	if err != nil || result.FromCache || result.Data.Version != 1 || tvdbSettingCounts(t, pool) != ([7]int{1, 1, 1, 1, 0, 0, 0}) {
		t.Fatal("retry after rollback failed", err)
	}
}
