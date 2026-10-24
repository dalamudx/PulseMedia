package settings_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/artwork"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/settings"
	"github.com/jackc/pgx/v5/pgxpool"
)

func artworkSettingCounts(t *testing.T, pool *pgxpool.Pool) [7]int {
	t.Helper()
	var c [7]int
	if err := pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM setting WHERE key IN ('fanart.api_key','fanart.artwork')),(SELECT count(*) FROM idempotency_record),(SELECT count(*) FROM audit_log),(SELECT count(*) FROM outbox_event),(SELECT count(*) FROM job),(SELECT count(*) FROM download_task),(SELECT count(*) FROM transfer_plan)`).Scan(&c[0], &c[1], &c[2], &c[3], &c[4], &c[5], &c[6]); err != nil {
		t.Fatal(err)
	}
	return c
}
func TestFanartEncryptedConfigurationAndReceiptsPG(t *testing.T) {
	pool, svc, q, admin, member := setupSettingsPoolTest(t)
	ctx := t.Context()
	plain := "fixture-fanart-credential"
	req := settings.UpdateSettingRequest{Value: map[string]any{"api_key": plain}}
	start := make(chan struct{})
	failures := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.UpdateSetting(ctx, admin, artwork.APIKeySetting, req, "fanart-original")
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
	if artworkSettingCounts(t, pool) != ([7]int{1, 1, 1, 1, 0, 0, 0}) {
		t.Fatal("duplicate receipt or task created")
	}
	row, err := q.GetSetting(ctx, artwork.APIKeySetting)
	if err != nil || !artwork.ValidCiphertext(row.Value) || bytes.Contains(row.Value, []byte(plain)) {
		t.Fatal("plaintext persisted", err)
	}
	if secret, err := svc.GetSecret(ctx, artwork.APIKeySetting); err != nil || secret != plain {
		t.Fatal("decryption failed", err)
	}
	masked, err := svc.GetSetting(ctx, artwork.APIKeySetting)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := svc.ReadSettingReceipt(ctx, admin, artwork.APIKeySetting, "fanart-original")
	if err != nil || !receipt.FromCache {
		t.Fatal("receipt failed", err)
	}
	items, err := svc.ListSettings(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []any{masked, receipt, items} {
		body, e := json.Marshal(item)
		if e != nil || bytes.Contains(body, []byte(plain)) || bytes.Contains(body, []byte("ciphertext")) {
			t.Fatal("public secret exposed")
		}
	}
	var ledgers string
	if err := pool.QueryRow(ctx, `SELECT concat((SELECT string_agg(payload::text,'') FROM audit_log),(SELECT string_agg(payload::text,'') FROM outbox_event),(SELECT string_agg(response_body::text,'') FROM idempotency_record))`).Scan(&ledgers); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ledgers, plain) || strings.Contains(ledgers, "ciphertext") {
		t.Fatal("ledger contains secret")
	}
	offline := settings.NewService(postgres.NewSettingsStore(pool, idgen.New()))
	if _, err := offline.ReadSettingReceipt(ctx, admin, artwork.APIKeySetting, "fanart-original"); err != nil {
		t.Fatal("receipt needs decryptor", err)
	}
	if _, err := offline.UpdateSetting(ctx, admin, artwork.SwitchSetting, settings.UpdateSettingRequest{Value: map[string]any{"enabled": false}}, "fanart-disable"); err != nil {
		t.Fatal("offline disable failed", err)
	}
	after, err := q.GetSetting(ctx, artwork.APIKeySetting)
	if err != nil || !bytes.Equal(row.Value, after.Value) || after.Version != row.Version {
		t.Fatal("disable rewrote ciphertext")
	}
	if _, err := svc.ReadSettingReceipt(ctx, member, artwork.APIKeySetting, "fanart-original"); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("member read receipt", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE app_user SET role='member' WHERE id=$1`, admin.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ReadSettingReceipt(ctx, admin, artwork.APIKeySetting, "fanart-original"); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("revoked owner read receipt", err)
	}
}

func TestFanartInvalidConfigurationHasNoWritesPG(t *testing.T) {
	pool, svc, _, admin, _ := setupSettingsPoolTest(t)
	for _, value := range []any{nil, true, map[string]any{}, map[string]any{"enabled": nil}, map[string]any{"enabled": "true"}, map[string]any{"enabled": true, "api_key": "plain"}} {
		_, err := svc.UpdateSetting(t.Context(), admin, artwork.SwitchSetting, settings.UpdateSettingRequest{Value: value}, "bad-switch")
		if !errors.Is(err, ports.ErrInvalidSettingCommand) {
			t.Fatal("invalid switch accepted", err)
		}
	}
	for _, value := range []any{map[string]any{"api_key": ""}, map[string]any{"api_key": "a\nb"}, map[string]any{"api_key": strings.Repeat("x", 513)}, map[string]any{"api_key": "ok", "url": "https://wrong.invalid"}} {
		_, err := svc.UpdateSetting(t.Context(), admin, artwork.APIKeySetting, settings.UpdateSettingRequest{Value: value}, "bad-key")
		if !errors.Is(err, ports.ErrInvalidSettingCommand) {
			t.Fatal("invalid credential accepted", err)
		}
	}
	if artworkSettingCounts(t, pool) != ([7]int{}) {
		t.Fatal("invalid configuration left writes")
	}
}

func TestFanartConfigurationTailRollbackPG(t *testing.T) {
	pool, svc, _, admin, _ := setupSettingsPoolTest(t)
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_fanart_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fanart-tail'; END $$; CREATE TRIGGER reject_fanart_receipt BEFORE INSERT ON idempotency_record FOR EACH ROW EXECUTE FUNCTION reject_fanart_receipt()`); err != nil {
		t.Fatal(err)
	}
	request := settings.UpdateSettingRequest{Value: map[string]any{"api_key": "fixture-fanart-key"}}
	if _, err := svc.UpdateSetting(ctx, admin, artwork.APIKeySetting, request, "fanart-tail"); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal("tail error contract lost", err)
	}
	if artworkSettingCounts(t, pool) != ([7]int{}) {
		t.Fatal("tail failure left partial configuration")
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER reject_fanart_receipt ON idempotency_record`); err != nil {
		t.Fatal(err)
	}
	result, err := svc.UpdateSetting(ctx, admin, artwork.APIKeySetting, request, "fanart-tail")
	if err != nil || result.FromCache || result.Data.Version != 1 || artworkSettingCounts(t, pool) != ([7]int{1, 1, 1, 1, 0, 0, 0}) {
		t.Fatal("original-key retry after rollback failed", err)
	}
}
