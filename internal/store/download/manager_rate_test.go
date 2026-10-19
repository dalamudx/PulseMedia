package download

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ratePublisherClient struct {
	get   func(context.Context) (*ports.TorrentInfo, error)
	calls atomic.Int32
}

func (c *ratePublisherClient) GetTorrent(ctx context.Context, _ string) (*ports.TorrentInfo, error) {
	c.calls.Add(1)
	return c.get(ctx)
}
func (*ratePublisherClient) AddTorrent(context.Context, ports.AddTorrentParam) error {
	panic("unexpected Add")
}
func (*ratePublisherClient) RemoveTorrent(context.Context, string, bool) error {
	panic("unexpected Remove")
}
func (*ratePublisherClient) ListTorrents(context.Context, string) ([]*ports.TorrentInfo, error) {
	panic("unexpected List")
}
func (*ratePublisherClient) Test(context.Context) error { panic("unexpected Test") }

type ratePublisherFixture struct {
	pool   *pgxpool.Pool
	repo   *Repository
	task   db.DownloadTask
	config db.DownloadClient
	info   ports.TorrentInfo
	client *ratePublisherClient
}

func ratePublisherExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatal(err)
	}
}
func newRatePublisherFixture(t *testing.T) *ratePublisherFixture {
	t.Helper()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	if _, err := postgres.Migrate(t.Context(), dsn, "../../../migrations"); err != nil {
		t.Fatal(err)
	}
	pool, err := postgres.NewPool(t.Context(), dsn, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var version string
	if err = pool.QueryRow(t.Context(), "SHOW server_version").Scan(&version); err != nil || !strings.HasPrefix(version, "18.6") {
		t.Fatalf("PG18.6 required: %q %v", version, err)
	}
	source, candidate, client, task, op := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	ratePublisherExec(t, pool, `INSERT INTO indexer(id,name,url,api_key_encrypted) VALUES($1,'rate fixture','https://rate.invalid','fixture')`, source)
	ratePublisherExec(t, pool, `INSERT INTO release_candidate(id,indexer_id,title,guid,link_encrypted,size_bytes) VALUES($1,$2,'rate fixture','rate fixture','fixture',100)`, candidate, source)
	key := make([]byte, 32)
	cipher, err := crypto.Encrypt(key, []byte("fixture-password"))
	if err != nil {
		t.Fatal(err)
	}
	ratePublisherExec(t, pool, `INSERT INTO download_client(id,name,client_type,url,username,password_encrypted,remote_path_map) VALUES($1,'rate fixture','qbittorrent','https://rate.invalid','fixture-user',$2,'{}')`, client, cipher)
	hash := strings.Repeat("a", 40)
	ratePublisherExec(t, pool, `INSERT INTO download_task(id,operation_id,client_id,candidate_id,info_hash,state,save_path,content_path,progress,total_bytes,downloaded_bytes) VALUES($1,$2,$3,$4,$5,'tracking','/downloads','/downloads/item.mkv',0.1,100,10)`, task, op, client, candidate, hash)
	q := db.New(pool)
	original, err := q.GetDownloadTaskByOpID(t.Context(), op)
	if err != nil {
		t.Fatal(err)
	}
	config, err := q.GetDownloadClientByID(t.Context(), client)
	if err != nil {
		t.Fatal(err)
	}
	f := &ratePublisherFixture{pool: pool, repo: NewRepositoryWithDownloadRates(pool, idgen.New(), key), task: original, config: config,
		info: ports.TorrentInfo{InfoHash: hash, SavePath: "/downloads", ContentPath: "/downloads/item.mkv", Progress: 0.25, TotalBytes: 100, DownloadedBytes: 25, State: "downloading"}}
	f.client = &ratePublisherClient{get: func(context.Context) (*ports.TorrentInfo, error) { return &f.info, nil }}
	f.repo.RegisterClient(client, f.client)
	return f
}
func (f *ratePublisherFixture) taskJSON(t *testing.T) string {
	t.Helper()
	var result string
	if err := f.pool.QueryRow(t.Context(), `SELECT to_jsonb(t)::text FROM download_task t WHERE id=$1`, f.task.ID).Scan(&result); err != nil {
		t.Fatal(err)
	}
	return result
}
func (f *ratePublisherFixture) sample(t *testing.T) (pgtype.Int8, string) {
	t.Helper()
	var value pgtype.Int8
	var task string
	if err := f.pool.QueryRow(t.Context(), `SELECT r.download_rate_bytes_per_second,to_jsonb(r)::text FROM download_rate_observation r WHERE task_id=$1`, f.task.ID).Scan(&value, &task); err != nil {
		t.Fatal(err)
	}
	return value, task
}
func (f *ratePublisherFixture) protected(t *testing.T) map[string]string {
	t.Helper()
	rows, err := f.pool.Query(t.Context(), `SELECT tablename FROM pg_tables WHERE schemaname='public' AND tablename ~ '(reservation|requirement|job|event|fact)' ORDER BY tablename`)
	if err != nil {
		t.Fatal(err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]string{}
	for _, name := range names {
		var value string
		err = f.pool.QueryRow(t.Context(), `SELECT coalesce(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM `+pgx.Identifier{name}.Sanitize()+` t`).Scan(&value)
		if err != nil {
			t.Fatal(err)
		}
		result[name] = value
	}
	return result
}

func TestRatePublisherProgressAndNetworkUnknownPG(t *testing.T) {
	f := newRatePublisherFixture(t)
	before := f.protected(t)
	for _, rate := range []*int64{new(int64(math.MaxInt64)), new(int64(0)), nil, new(int64(-1))} {
		f.info.DownloadRateBytesPerSecond = rate
		got, err := f.repo.ObserveTask(t.Context(), f.task.OperationID)
		if err != nil || got == nil || got.State != "tracking" || got.Progress != 0.25 || got.TotalBytes != 100 || got.DownloadedBytes != 25 || got.ContentPath != "/downloads/item.mkv" {
			t.Fatalf("progress: %+v %v", got, err)
		}
		value, _ := f.sample(t)
		valid := rate != nil && *rate >= 0
		if value.Valid != valid || (valid && value.Int64 != *rate) {
			t.Fatalf("native rate: %+v", value)
		}
	}
	f.info.DownloadRateBytesPerSecond = new(int64(77))
	if _, err := f.repo.ObserveTask(t.Context(), f.task.OperationID); err != nil {
		t.Fatal(err)
	}
	taskBefore := f.taskJSON(t)
	f.client.get = func(context.Context) (*ports.TorrentInfo, error) { return nil, ports.ErrTimeout }
	if _, err := f.repo.ObserveTask(t.Context(), f.task.OperationID); !errors.Is(err, ports.ErrTimeout) {
		t.Fatalf("transport error: %v", err)
	}
	value, _ := f.sample(t)
	if value.Valid || f.taskJSON(t) != taskBefore {
		t.Fatal("network failure retained speed or mutated task")
	}
	if !reflect.DeepEqual(before, f.protected(t)) {
		t.Fatal("business graph changed")
	}
	if f.client.calls.Load() != 6 {
		t.Fatal("extra native calls", f.client.calls.Load())
	}
}

func TestRatePublisherHeldCallbackFencePG(t *testing.T) {
	for _, change := range []string{"config", "task", "rename"} {
		t.Run(change, func(t *testing.T) {
			f := newRatePublisherFixture(t)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			f.client.get = func(context.Context) (*ports.TorrentInfo, error) { close(entered); <-release; return &f.info, nil }
			done := make(chan error, 1)
			go func() { _, err := f.repo.ObserveTask(t.Context(), f.task.OperationID); done <- err }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("native call not entered")
			}
			switch change {
			case "config":
				ratePublisherExec(t, f.pool, `UPDATE download_client SET url='https://changed.invalid' WHERE id=$1`, f.task.ClientID)
			case "task":
				ratePublisherExec(t, f.pool, `UPDATE download_task SET progress=0.5,updated_at=updated_at+interval '1 second' WHERE id=$1`, f.task.ID)
			case "rename":
				ratePublisherExec(t, f.pool, `UPDATE download_client SET name='renamed',updated_at=clock_timestamp() WHERE id=$1`, f.task.ClientID)
			}
			before := f.taskJSON(t)
			unblock()
			select {
			case err := <-done:
				if change == "rename" {
					if err != nil {
						t.Fatal(err)
					}
					f.sample(t)
				} else {
					if !errors.Is(err, ports.ErrDownloadTelemetryStale) {
						t.Fatalf("late callback: %v", err)
					}
					var count int
					if e := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM download_rate_observation`).Scan(&count); e != nil {
						t.Fatal(e)
					}
					if count != 0 || f.taskJSON(t) != before {
						t.Fatal("stale callback wrote")
					}
				}
			case <-time.After(5 * time.Second):
				t.Fatal("callback did not return")
			}
		})
	}
}

func TestRatePublisherSnapshotFactoryAndDirectValidation(t *testing.T) {
	key := make([]byte, 32)
	cipher, err := crypto.Encrypt(key, []byte("snapshot-password"))
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(nil, idgen.New(), key)
	config := db.DownloadClient{ID: uuid.New(), ClientType: "transmission", Url: "https://snapshot.invalid", Username: "snapshot-user", PasswordEncrypted: cipher}
	client := &ratePublisherClient{}
	calls := 0
	m.SetClientFactory(func(kind, endpoint, user, password string) (ports.DownloadClient, error) {
		calls++
		if kind != config.ClientType || endpoint != config.Url || user != config.Username || password != "snapshot-password" {
			t.Fatal("factory did not use supplied snapshot")
		}
		return client, nil
	})
	got, err := m.clientForRateSnapshot(t.Context(), config)
	if err != nil || got != client || calls != 1 {
		t.Fatal("snapshot factory", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = m.clientForRateSnapshot(ctx, config); !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatal("constructed canceled client", err)
	}
	config.PasswordEncrypted = []byte("malformed")
	if _, err = m.clientForRateSnapshot(t.Context(), config); err != ports.ErrUnavailable || calls != 1 {
		t.Fatal("constructed invalid credentials", err)
	}
	m.RegisterClient(config.ID, client)
	if got, err = m.clientForRateSnapshot(t.Context(), config); err != nil || got != client {
		t.Fatal("injected capability rejected", err)
	}
	original := db.DownloadTask{InfoHash: strings.Repeat("a", 40), SavePath: "/downloads"}
	valid := ports.TorrentInfo{InfoHash: original.InfoHash, SavePath: original.SavePath, Progress: 0.25, TotalBytes: 100, DownloadedBytes: 25}
	for _, info := range []*ports.TorrentInfo{nil, {}, {InfoHash: original.InfoHash, SavePath: "/elsewhere"},
		{InfoHash: original.InfoHash, SavePath: original.SavePath, Progress: float32(math.NaN())},
		{InfoHash: original.InfoHash, SavePath: original.SavePath, Progress: 1, TotalBytes: 100, DownloadedBytes: 100, State: "seeding"}} {
		if _, e := m.updateProgressWithRate(t.Context(), original, config, info); e != ports.ErrDownloadTelemetryStale {
			t.Fatal("invalid direct observation accepted", e)
		}
	}
	valid.ContentPath = "/outside/item.mkv"
	if _, e := m.updateProgressWithRate(t.Context(), original, config, &valid); e != ports.ErrDownloadTelemetryStale {
		t.Fatal(e)
	}
}

func TestRatePublisherCanceledPhysicalSlotPG(t *testing.T) {
	f := newRatePublisherFixture(t)
	before := f.taskJSON(t)
	entered, release := make(chan struct{}, 2), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	f.client.get = func(context.Context) (*ports.TorrentInfo, error) {
		entered <- struct{}{}
		<-release
		return &f.info, nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 2)
	for range 2 {
		go func() { _, err := f.repo.ObserveTask(ctx, f.task.OperationID); done <- err }()
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("native slots not occupied")
		}
	}
	cancel()
	select {
	case err := <-done:
		t.Fatalf("returned before physical I/O: %v", err)
	default:
	}
	f.repo.execution.mu.RLock()
	occupied := len(f.repo.execution.slots[f.task.ClientID])
	f.repo.execution.mu.RUnlock()
	if occupied != 2 {
		t.Fatalf("physical slots released: %d", occupied)
	}
	wait, stop := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer stop()
	if _, err := f.repo.ObserveTask(wait, f.task.OperationID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting callback: %v", err)
	}
	if f.client.calls.Load() != 2 {
		t.Fatal("canceled callback released capacity")
	}
	unblock()
	for range 2 {
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("callback did not finish")
		}
	}
	if f.taskJSON(t) != before {
		t.Fatal("canceled callback wrote progress")
	}
	var count int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM download_rate_observation`).Scan(&count); err != nil || count != 0 {
		t.Fatal("canceled callback wrote rate", err)
	}
}

func TestRatePublisherDeferredTailRollbackPG(t *testing.T) {
	f := newRatePublisherFixture(t)
	f.info.DownloadRateBytesPerSecond = new(int64(99))
	if _, err := f.repo.ObserveTask(t.Context(), f.task.OperationID); err != nil {
		t.Fatal(err)
	}
	beforeTask := f.taskJSON(t)
	_, beforeRate := f.sample(t)
	beforeGraph := f.protected(t)
	ratePublisherExec(t, f.pool, `CREATE FUNCTION rate_publisher_fail_tail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture tail failure'; END $$`)
	ratePublisherExec(t, f.pool, `CREATE CONSTRAINT TRIGGER rate_publisher_tail AFTER INSERT OR UPDATE ON download_rate_observation DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION rate_publisher_fail_tail()`)
	f.info.Progress = 0.5
	f.info.DownloadedBytes = 50
	f.info.DownloadRateBytesPerSecond = new(int64(123))
	if _, err := f.repo.ObserveTask(t.Context(), f.task.OperationID); err != ports.ErrUnavailable {
		t.Fatalf("unsafe tail error: %v", err)
	}
	_, afterRate := f.sample(t)
	if f.taskJSON(t) != beforeTask || afterRate != beforeRate || !reflect.DeepEqual(beforeGraph, f.protected(t)) {
		t.Fatal("tail failure partially committed")
	}
	f.client.get = func(context.Context) (*ports.TorrentInfo, error) { return nil, ports.ErrTimeout }
	if _, err := f.repo.ObserveTask(t.Context(), f.task.OperationID); err != ports.ErrUnavailable {
		t.Fatalf("unknown tail error: %v", err)
	}
	_, afterRate = f.sample(t)
	if f.taskJSON(t) != beforeTask || afterRate != beforeRate {
		t.Fatal("unknown tail failure partially committed")
	}
}

func TestRatePublisherLegacyWithoutSchema93PG(t *testing.T) {
	f := newRatePublisherFixture(t)
	legacy := NewRepository(f.pool, idgen.New(), make([]byte, 32))
	legacy.RegisterClient(f.task.ClientID, f.client)
	ratePublisherExec(t, f.pool, `DROP TABLE download_rate_observation`)
	if legacy.execution.recordRates {
		t.Fatal("legacy rates enabled")
	}
	if got, err := legacy.ObserveTask(t.Context(), f.task.OperationID); err != nil || got == nil || got.Progress != 0.25 {
		t.Fatalf("legacy schema dependency: %+v %v", got, err)
	}
}
