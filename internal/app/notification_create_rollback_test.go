package app

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
	"strings"
	"testing"
)

func TestNotificationCreateReceiptFailureRollsBackAndRecovers(t *testing.T) {
	f := organizationSetup(t)
	ctx := context.Background()
	_, _, n := testOutboxServices(f)
	actor := notificationFixtureAdmin(t, f)
	cfg := notify.ChannelConfig{URL: "https://example.test/private-destination", Secret: "private-tail-secret"}
	if _, e := f.pool.Exec(ctx, `CREATE FUNCTION reject_channel_create_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.idempotency_key='create_tail' THEN RAISE EXCEPTION 'fixture failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_channel_create_receipt BEFORE INSERT ON idempotency_record FOR EACH ROW EXECUTE FUNCTION reject_channel_create_receipt()`); e != nil {
		t.Fatal(e)
	}
	if _, e := n.CreateChannel(ctx, actor, "Tail", "webhook", cfg, "create_tail"); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal("tail failure accepted", e)
	}
	var channels, audits, receipts int
	if e := f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM notification_channel), (SELECT count(*) FROM audit_log WHERE action='notification_channel.created'), (SELECT count(*) FROM idempotency_record WHERE idempotency_key='create_tail')`).Scan(&channels, &audits, &receipts); e != nil || channels != 0 || audits != 0 || receipts != 0 {
		t.Fatalf("partial recipient after failure %d %d %d: %v", channels, audits, receipts, e)
	}
	if _, e := f.pool.Exec(ctx, `DROP TRIGGER reject_channel_create_receipt ON idempotency_record; DROP FUNCTION reject_channel_create_receipt()`); e != nil {
		t.Fatal(e)
	}
	row, e := n.CreateChannel(ctx, actor, "Tail", "webhook", cfg, "create_tail")
	if e != nil {
		t.Fatal(e)
	}
	again, e := n.CreateChannel(ctx, actor, "Tail", "webhook", cfg, "create_tail")
	if e != nil || again.ID != row.ID {
		t.Fatal("same key created another channel", e)
	}
	var hash string
	if e = f.pool.QueryRow(ctx, `SELECT request_hash FROM idempotency_record WHERE idempotency_key='create_tail'`).Scan(&hash); e != nil || !strings.HasPrefix(hash, "hmac.v1:") {
		t.Fatal("secret-bearing intent not keyed", e)
	}
	if e = f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM notification_channel), (SELECT count(*) FROM audit_log WHERE action='notification_channel.created'), (SELECT count(*) FROM idempotency_record WHERE idempotency_key='create_tail')`).Scan(&channels, &audits, &receipts); e != nil || channels != 1 || audits != 1 || receipts != 1 {
		t.Fatalf("retry duplicated recipient %d %d %d: %v", channels, audits, receipts, e)
	}
}
