package postgres_test

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDownloadRateClientStamp(t *testing.T) {
	c := db.DownloadClient{ID: uuid.New(), ClientType: "qbittorrent", Url: "https://private.invalid/path", Username: "private", PasswordEncrypted: []byte("cipher"), RemotePathMap: []byte(`{"/b":"/two","/a":"/one"}`)}
	stamp, err := postgres.DownloadRateClientStamp(c)
	if err != nil || len(stamp) != 64 || strings.Contains(stamp, "private") {
		t.Fatal("invalid safe stamp", err)
	}
	same := c
	same.Name = "renamed"
	same.CreatedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	same.UpdatedAt = same.CreatedAt
	same.RemotePathMap = []byte(`{ "/a": "/one", "/b": "/two" }`)
	if got, e := postgres.DownloadRateClientStamp(same); e != nil || got != stamp {
		t.Fatal("cosmetic configuration changed stamp", e)
	}
	for _, change := range []func(*db.DownloadClient){
		func(c *db.DownloadClient) { c.ID = uuid.New() },
		func(c *db.DownloadClient) { c.ClientType = "transmission" },
		func(c *db.DownloadClient) { c.Url += "/changed" },
		func(c *db.DownloadClient) { c.Username += "changed" },
		func(c *db.DownloadClient) { c.PasswordEncrypted = []byte("new cipher") },
		func(c *db.DownloadClient) { c.RemotePathMap = []byte(`{"/a":"/changed"}`) },
	} {
		changed := c
		change(&changed)
		if got, e := postgres.DownloadRateClientStamp(changed); e != nil || got == stamp {
			t.Fatal("material config not fingerprinted", e)
		}
	}
	c.RemotePathMap = nil
	empty, _ := postgres.DownloadRateClientStamp(c)
	for _, raw := range []string{`{}`, `null`} {
		c.RemotePathMap = []byte(raw)
		got, e := postgres.DownloadRateClientStamp(c)
		if e != nil || got != empty {
			t.Fatal("empty map normalization", e)
		}
	}
	c.RemotePathMap = []byte(`{"secret":`)
	if got, e := postgres.DownloadRateClientStamp(c); got != "" || e != ports.ErrDownloadTelemetryStale {
		t.Fatal("unsafe malformed map error", e)
	}
}

func rateTask(t *testing.T, pool *pgxpool.Pool, op uuid.UUID) (db.DownloadTask, string) {
	t.Helper()
	q := db.New(pool)
	task, err := q.GetDownloadTaskByOpID(t.Context(), op)
	if err != nil {
		t.Fatal(err)
	}
	client, err := q.GetDownloadClientByID(t.Context(), task.ClientID)
	if err != nil {
		t.Fatal(err)
	}
	stamp, err := postgres.DownloadRateClientStamp(client)
	if err != nil {
		t.Fatal(err)
	}
	return task, stamp
}
func rateRecord(t *testing.T, pool *pgxpool.Pool, task db.DownloadTask, stamp string, rate *int64, source string, commit bool) error {
	t.Helper()
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	err = postgres.RecordDownloadRate(t.Context(), tx, task, stamp, rate, source)
	if err == nil && commit {
		err = tx.Commit(t.Context())
	}
	return err
}
func rateRead(t *testing.T, pool *pgxpool.Pool, ids ...uuid.UUID) map[uuid.UUID]ports.OperationTelemetry {
	t.Helper()
	tx, err := pool.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	out, err := postgres.ReadOperationTelemetry(t.Context(), db.New(tx), ids)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return out
}
func TestDownloadRateStoragePG(t *testing.T) {
	f := newDashboardActivityFixture(t)
	op := f.add(t, f.actor.ID, "tracking", time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC))
	task, stamp := rateTask(t, f.pool, op)
	before := dashboardActivityGraph(t, f.pool)
	unknown := uuid.New()
	got := rateRead(t, f.pool, op, unknown)
	if len(got) != 2 || got[op].RateStatus != "unknown" || got[unknown].Progress != nil || got[unknown].RateStatus != "unknown" {
		t.Fatal("default map", got)
	}
	for _, value := range []int64{math.MaxInt64, 0, -1} {
		if err := rateRecord(t, f.pool, task, stamp, &value, "ordinary", true); err != nil {
			t.Fatal(err)
		}
		item := rateRead(t, f.pool, op)[op]
		if item.Progress == nil || *item.Progress != 0.25 || item.ProgressSource != "native_percent" || item.RateObservedAt == nil {
			t.Fatal("lost verified progress/sample", item)
		}
		if value < 0 {
			if item.RateStatus != "unknown" || item.DownloadRateBytesPerSecond != nil {
				t.Fatal("negative sample", item)
			}
		} else {
			want := "9223372036854775807"
			if value == 0 {
				want = "0"
			}
			if item.RateStatus != "current" || item.DownloadRateBytesPerSecond == nil || *item.DownloadRateBytesPerSecond != want {
				t.Fatal("exact bigint", item)
			}
		}
	}
	if err := rateRecord(t, f.pool, task, stamp, nil, "ordinary", true); err != nil {
		t.Fatal(err)
	}
	item := rateRead(t, f.pool, op)[op]
	if item.RateStatus != "unknown" || item.DownloadRateBytesPerSecond != nil || item.RateObservedAt == nil {
		t.Fatal("latest null revived old rate", item)
	}
	value := int64(42)
	if err := rateRecord(t, f.pool, task, stamp, &value, "ordinary", true); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		age, status string
		keepTime    bool
	}{{"-46 seconds", "stale", true}, {"-44 seconds", "current", true}, {"1 hour", "unknown", false}, {"infinity", "unknown", false}} {
		if tc.age == "infinity" {
			execIntent(t, f.pool, `UPDATE download_rate_observation SET observed_at='infinity' WHERE task_id=$1`, task.ID)
		} else {
			execIntent(t, f.pool, `UPDATE download_rate_observation SET observed_at=clock_timestamp()+$2::interval WHERE task_id=$1`, task.ID, tc.age)
		}
		item = rateRead(t, f.pool, op)[op]
		if item.RateStatus != tc.status || (item.RateObservedAt != nil) != tc.keepTime || (item.DownloadRateBytesPerSecond != nil) != (tc.status != "unknown") {
			t.Fatal("age", tc, item)
		}
	}
	for _, tc := range []struct {
		name           string
		change         func(*db.DownloadTask)
		source, client string
	}{
		{name: "id", change: func(d *db.DownloadTask) { d.ID = uuid.New() }},
		{name: "operation", change: func(d *db.DownloadTask) { d.OperationID = uuid.New() }},
		{name: "client", change: func(d *db.DownloadTask) { d.ClientID = uuid.New() }},
		{name: "hash", change: func(d *db.DownloadTask) { d.InfoHash += "changed" }},
		{name: "path", change: func(d *db.DownloadTask) { d.SavePath += "changed" }},
		{name: "state", change: func(d *db.DownloadTask) { d.State = "unknown" }},
		{name: "date", change: func(d *db.DownloadTask) { d.UpdatedAt.Time = d.UpdatedAt.Time.Add(time.Microsecond) }},
		{name: "source", source: "automatic_tv"}, {name: "invalid source", source: "private invalid"}, {name: "config", client: strings.Repeat("0", 64)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := task
			if tc.change != nil {
				tc.change(&d)
			}
			s := stamp
			if tc.client != "" {
				s = tc.client
			}
			source := "ordinary"
			if tc.source != "" {
				source = tc.source
			}
			if err := rateRecord(t, f.pool, d, s, &value, source, true); !errors.Is(err, ports.ErrDownloadTelemetryStale) {
				t.Fatal("stale accepted", err)
			}
		})
	}
	// Time zones are representations, not task versions.
	task.UpdatedAt.Time = task.UpdatedAt.Time.In(time.FixedZone("offset", 9*60*60))
	if err := rateRecord(t, f.pool, task, stamp, &value, "ordinary", true); err != nil {
		t.Fatal("offset mismatch", err)
	}
	execIntent(t, f.pool, `UPDATE download_client SET name='renamed',updated_at=clock_timestamp(),remote_path_map='{}' WHERE id=$1`, task.ClientID)
	if err := rateRecord(t, f.pool, task, stamp, &value, "ordinary", true); err != nil {
		t.Fatal("cosmetic config", err)
	}
	// Compare only the intended task/history graph before changing config materially.
	assertEpisodeRecheckSnapshot(t, before, dashboardActivityGraph(t, f.pool))
	for _, update := range []string{`password_encrypted='changed'`, `remote_path_map='{"/remote":"/new"}'`, `url='https://changed.invalid'`} {
		execIntent(t, f.pool, "UPDATE download_client SET "+update+" WHERE id=$1", task.ClientID)
		if err := rateRecord(t, f.pool, task, stamp, &value, "ordinary", true); err != ports.ErrDownloadTelemetryStale {
			t.Fatal("stale config", err)
		}
		item = rateRead(t, f.pool, op)[op]
		if item.RateStatus != "unknown" || item.RateObservedAt != nil {
			t.Fatal("old config rate visible", item)
		}
		_, stamp = rateTask(t, f.pool, op)
		if err := rateRecord(t, f.pool, task, stamp, &value, "ordinary", true); err != nil {
			t.Fatal(err)
		}
	}
	execIntent(t, f.pool, `UPDATE download_task SET updated_at=updated_at+interval '1 second' WHERE id=$1`, task.ID)
	item = rateRead(t, f.pool, op)[op]
	if item.RateStatus != "unknown" || item.RateObservedAt != nil {
		t.Fatal("task version rate visible", item)
	}
	if err := rateRecord(t, f.pool, task, stamp, &value, "ordinary", true); err != ports.ErrDownloadTelemetryStale {
		t.Fatal("old callback", err)
	}
	task, stamp = rateTask(t, f.pool, op)
	for _, update := range []string{`info_hash=repeat('b',40)`, `save_path='/different'`, `state='unknown'`} {
		execIntent(t, f.pool, "UPDATE download_task SET "+update+" WHERE id=$1", task.ID)
		item = rateRead(t, f.pool, op)[op]
		if item.RateStatus != "unknown" || item.RateObservedAt != nil {
			t.Fatal("task identity rate visible", item)
		}
	}
}

func TestDownloadRateTransactionsAndRoundtripPG(t *testing.T) {
	f := newDashboardActivityFixture(t, 93)
	op := f.add(t, f.actor.ID, "tracking", time.Now().UTC())
	task, stamp := rateTask(t, f.pool, op)
	before := dashboardActivityGraph(t, f.pool)
	value := int64(9)
	if err := rateRecord(t, f.pool, task, stamp, &value, "ordinary", false); err != nil {
		t.Fatal(err)
	}
	if item := rateRead(t, f.pool, op)[op]; item.DownloadRateBytesPerSecond != nil {
		t.Fatal("rollback leaked", item)
	}
	// A deferred tail failure rolls back both business and display writes.
	execIntent(t, f.pool, `CREATE FUNCTION rate_commit_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'private storage failure'; END $$`)
	execIntent(t, f.pool, `CREATE CONSTRAINT TRIGGER rate_commit_failure AFTER INSERT OR UPDATE ON download_rate_observation DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION rate_commit_failure()`)
	tx, err := f.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := db.New(tx)
	if _, err = q.LockDownloadClientForEdit(t.Context(), task.ClientID); err != nil {
		t.Fatal(err)
	}
	updated, err := q.UpdateDownloadTaskProgress(t.Context(), db.UpdateDownloadTaskProgressParams{ID: task.ID, State: "tracking", Progress: 0.75, TotalBytes: 100, DownloadedBytes: 75, ContentPath: task.ContentPath})
	if err != nil {
		t.Fatal(err)
	}
	if err = postgres.RecordDownloadRate(t.Context(), tx, updated, stamp, &value, "ordinary"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err == nil {
		t.Fatal("tail commit unexpectedly succeeded")
	}
	assertEpisodeRecheckSnapshot(t, before, dashboardActivityGraph(t, f.pool))
	if item := rateRead(t, f.pool, op)[op]; item.DownloadRateBytesPerSecond != nil || item.Progress == nil || *item.Progress != 0.25 {
		t.Fatal("tail failure leaked", item)
	}
	execIntent(t, f.pool, `DROP TRIGGER rate_commit_failure ON download_rate_observation`)
	execIntent(t, f.pool, `DROP FUNCTION rate_commit_failure()`)
	if err = rateRecord(t, f.pool, task, stamp, &value, "ordinary", true); err != nil {
		t.Fatal(err)
	}
	dir := repositoryMigrationsThrough(t, 93)
	dsn := f.pool.Config().ConnString()
	if v, e := postgres.Rollback(t.Context(), dsn, dir); e != nil || v != 92 {
		t.Fatal("93 to92", v, e)
	}
	assertEpisodeRecheckSnapshot(t, before, dashboardActivityGraph(t, f.pool))
	if v, e := postgres.Migrate(t.Context(), dsn, dir); e != nil || v != 93 {
		t.Fatal("92 to93", v, e)
	}
	if item := rateRead(t, f.pool, op)[op]; item.RateStatus != "unknown" || item.DownloadRateBytesPerSecond != nil {
		t.Fatal("roundtrip telemetry", item)
	}
	assertEpisodeRecheckSnapshot(t, before, dashboardActivityGraph(t, f.pool))
}

func TestDownloadRateAutomaticTVVerifiedPG(t *testing.T) {
	f := dashboardActivityLargeTVFixture(t)
	if v, e := postgres.Migrate(t.Context(), f.pool.Config().ConnString(), repositoryMigrationsThrough(t, 93)); e != nil || v != 93 {
		t.Fatal(v, e)
	}
	op := f.request.OperationID
	f.claim(t)
	s := f.load(t)
	remote := automaticTVObservationRemote(s, false)
	remote.Before[0].VerifiedBytes = s.TotalBytes - 1
	remote.After[0].VerifiedBytes = s.TotalBytes - 1
	if receipt, e := f.observer.CommitAutomaticTVObservation(t.Context(), *s, remote); e != nil || receipt.State != "tracking" || receipt.ReviewRequired {
		t.Fatal(receipt, e)
	}
	task, stamp := rateTask(t, f.pool, op)
	value := int64(math.MaxInt64)
	if e := rateRecord(t, f.pool, task, stamp, &value, "ordinary", true); e != ports.ErrDownloadTelemetryStale {
		t.Fatal("wrong automatic source", e)
	}
	before := dashboardActivityGraph(t, f.pool)
	if e := rateRecord(t, f.pool, task, stamp, &value, "automatic_tv", true); e != nil {
		t.Fatal(e)
	}
	item := rateRead(t, f.pool, op)[op]
	if item.Progress == nil || *item.Progress != math.Nextafter(1, 0) || item.ProgressSource != "verified_bytes" || item.RateStatus != "current" {
		t.Fatal("maxint64 ratio/rate", item)
	}
	if e := rateRecord(t, f.pool, task, stamp, nil, "automatic_tv", true); e != nil {
		t.Fatal(e)
	}
	item = rateRead(t, f.pool, op)[op]
	if item.RateStatus != "unknown" || item.DownloadRateBytesPerSecond != nil || item.RateObservedAt == nil {
		t.Fatal("unknown fell back", item)
	}
	assertEpisodeRecheckSnapshot(t, before, dashboardActivityGraph(t, f.pool))
	f.finishPermit(t)
	timer := time.NewTimer(2100 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	f.claim(t)
	latest := f.load(t)
	bad := automaticTVObservationRemote(latest, false)
	bad.After[0].VerifiedBytes = -1
	if receipt, e := f.observer.CommitAutomaticTVObservation(t.Context(), *latest, bad); e != nil || !receipt.ReviewRequired {
		t.Fatal(receipt, e)
	}
	item = rateRead(t, f.pool, op)[op]
	if item.Progress != nil || item.ProgressObservedAt != nil || item.ProgressSource != "" || item.RateStatus != "unknown" {
		t.Fatal("bad latest revived history", item)
	}
}

func TestDownloadRateReaderBounds(t *testing.T) {
	got, e := postgres.ReadOperationTelemetry(t.Context(), nil, nil)
	if e != nil || got == nil || len(got) != 0 {
		t.Fatal(got, e)
	}
	if got, e = postgres.ReadOperationTelemetry(t.Context(), nil, make([]uuid.UUID, 101)); got != nil || e != ports.ErrInvalidOperationQuery {
		t.Fatal(got, e)
	}
	if got, e = postgres.ReadOperationTelemetry(t.Context(), nil, []uuid.UUID{uuid.New()}); got != nil || e != ports.ErrUnavailable {
		t.Fatal(got, e)
	}
	if !reflect.DeepEqual(got, map[uuid.UUID]ports.OperationTelemetry(nil)) {
		t.Fatal("unexpected output")
	}
}

func TestDownloadRateLockingPG(t *testing.T) {
	f := newDashboardActivityFixture(t)
	op := f.add(t, f.actor.ID, "tracking", time.Now().UTC())
	task, stamp := rateTask(t, f.pool, op)
	for _, table := range []string{"download_client", "download_task"} {
		t.Run(table, func(t *testing.T) {
			holder, err := f.pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = holder.Rollback(context.Background()) }()
			id := task.ID
			if table == "download_client" {
				id = task.ClientID
			}
			if _, err = holder.Exec(t.Context(), "SELECT id FROM "+table+" WHERE id=$1 FOR UPDATE", id); err != nil {
				t.Fatal(err)
			}
			writer, err := f.pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = writer.Rollback(context.Background()) }()
			if _, err = writer.Exec(t.Context(), "SET LOCAL lock_timeout='100ms'"); err != nil {
				t.Fatal(err)
			}
			rate := int64(5)
			if err = postgres.RecordDownloadRate(t.Context(), writer, task, stamp, &rate, "ordinary"); err != ports.ErrUnavailable {
				t.Fatal("did not lock current row safely", err)
			}
			if err = writer.Rollback(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err = holder.Rollback(t.Context()); err != nil {
				t.Fatal(err)
			}
			if item := rateRead(t, f.pool, op)[op]; item.DownloadRateBytesPerSecond != nil {
				t.Fatal("lock failure published sample", item)
			}
		})
	}
}
