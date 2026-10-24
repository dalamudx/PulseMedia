package postgres_test

import (
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"testing"
)

func TestNotificationRobotSchema116HistoryPG(t *testing.T) {
	f := newCreationFixture(t, 116)
	history := repositoryMigrationsThrough(t, 116)
	if v, e := postgres.Rollback(t.Context(), f.pool.Config().ConnConfig.ConnString(), history); e != nil || v != 115 {
		t.Fatal("empty rollback", v, e)
	}
	if v, e := postgres.Migrate(t.Context(), f.pool.Config().ConnConfig.ConnString(), history); e != nil || v != 116 {
		t.Fatal("reinstall", v, e)
	}
	q := db.New(f.pool)
	for _, kind := range []string{"webhook", "telegram", "wechat", "dingtalk", "feishu", "bark"} {
		channel, e := q.CreateNotificationChannel(t.Context(), db.CreateNotificationChannelParams{ID: uuid.New(), Name: "owned " + kind, ChannelType: kind, ConfigEncrypted: make([]byte, 64)})
		if e != nil {
			t.Fatal("supported channel rejected", kind, e)
		}
		if _, e = f.pool.Exec(t.Context(), `UPDATE notification_channel SET channel_type='webhook' WHERE id=$1`, channel.ID); e == nil && kind != "webhook" {
			t.Fatal("historical destination rewritable")
		}
	}
	if _, e := postgres.Rollback(t.Context(), f.pool.Config().ConnConfig.ConnString(), history); e == nil {
		t.Fatal("robot history discarded")
	}
	var count int
	if e := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM notification_channel`).Scan(&count); e != nil || count != 6 {
		t.Fatal("failed down lost history", count, e)
	}
	t.Log("M10_NOTIFICATION_MIGRATION_PASS range=116_115_116 history=PROTECTED legacy_types=UNCHANGED")
}
