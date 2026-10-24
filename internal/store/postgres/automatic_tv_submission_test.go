package postgres_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type automaticTVSubmissionFixture struct {
	automaticTVFixture
	store    *postgres.AutomaticTVSubmissionStore
	request  ports.AutomaticTVSendRequest
	command  ports.PrepareAutomaticTVSubmissionCommand
	physical *pgxpool.Conn
}

func automaticTVSubmissionMetainfo(t *testing.T, name string) *ports.AutomaticTVMetainfoProof {
	t.Helper()
	raw := []byte("d4:infod6:lengthi1234e4:name" + strconv.Itoa(len(name)) + ":" + name + "12:piece lengthi16384e6:pieces20:" + strings.Repeat("0", 20) + "ee")
	p, e := ports.InspectAutomaticTVMetainfo(torrent.Inspector{}, raw)
	if e != nil {
		t.Fatal("native inspector", e)
	}
	return p
}

// Keep this owner's fixtures independent of coordinated schema90 publication.
// Only catalog fixtures are inserted here; queries, finalization and authority
// always pass through the actual Source87 / Finalizer88 implementations.
func automaticTVSubmissionBase(t *testing.T, explicit bool) automaticTVFixture {
	t.Helper()
	root := t.TempDir()
	migrations := filepath.Join(root, "migrations89")
	if e := os.Mkdir(migrations, 0700); e != nil {
		t.Fatal(e)
	}
	entries, e := os.ReadDir("../../../migrations")
	if e != nil {
		t.Fatal(e)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".sql") || len(name) < 5 {
			continue
		}
		version, e := strconv.ParseInt(name[:5], 10, 64)
		if e != nil || version > 89 {
			continue
		}
		raw, e := os.ReadFile(filepath.Join("../../../migrations", name))
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(migrations, name), raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	if v, e := postgres.Migrate(t.Context(), dsn, migrations); e != nil || v != 89 {
		t.Fatalf("pinned89 migration %d %v", v, e)
	}
	pool, e := postgres.NewPool(t.Context(), dsn, postgres.DefaultPoolConfig())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(pool.Close)
	var version string
	if e = pool.QueryRow(t.Context(), "SHOW server_version").Scan(&version); e != nil || !strings.HasPrefix(version, "18.6") {
		t.Fatal("requires PG18.6", version, e)
	}
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember}
	q := db.New(pool)
	if _, e = q.CreateUser(t.Context(), db.CreateUserParams{ID: uuid.MustParse(actor.ID), Username: "submission-owner", PasswordHash: "fixture", Role: ports.RoleMember, IsActive: true}); e != nil {
		t.Fatal(e)
	}
	base := creationFixture{pool: pool, store: postgres.NewSubscriptionCreationStore(pool, idgen.New()), actor: actor, movie: uuid.New(), tv: uuid.New(), path: root}
	for _, r := range []struct {
		id   uuid.UUID
		kind string
	}{{base.movie, "movie"}, {base.tv, "tv"}} {
		if _, e = q.CreateLibraryRoot(t.Context(), db.CreateLibraryRootParams{ID: r.id, Name: r.kind, Path: filepath.Join(root, r.kind), MediaType: r.kind}); e != nil {
			t.Fatal(e)
		}
	}
	codec := crypto.NewSecretProtector(bytes.Repeat([]byte{27}, 32))
	key, e := codec.EncryptSecret([]byte("fixture-api-key"))
	if e != nil {
		t.Fatal(e)
	}
	endpoint, e := codec.EncryptSecret([]byte("https://tv-source.test/api?apikey=private-query-key"))
	if e != nil {
		t.Fatal(e)
	}
	indexer, client := uuid.New(), uuid.New()
	execIntent(t, pool, `INSERT INTO indexer(id,name,url,api_key_encrypted,categories,health) VALUES($1,'native TV','https://tv-source.test',$2,'[5040,5000]','online')`, indexer, key)
	execIntent(t, pool, `INSERT INTO indexer_configuration(indexer_id,protocol,url_encrypted,behavior_version,configuration_version) VALUES($1,'torznab',$2,'explicit-v1',1)`, indexer, endpoint)
	execIntent(t, pool, `INSERT INTO indexer_lifecycle(indexer_id,enabled) VALUES($1,true)`, indexer)
	execIntent(t, pool, `INSERT INTO download_client(id,name,client_type,url,username,password_encrypted,remote_path_map) VALUES($1,'TV client','qbittorrent','https://downloader.test','fixture',$2,'{}')`, client, key)
	in := creationInput(55101, "tv")
	in.IndexerIDs = []uuid.UUID{indexer}
	in.DownloaderID = &client
	season := creationSeason(2, 3)
	// This schema89 send fixture supplies one known episode, without season-retirement authority.
	season.Complete = false
	aired := time.Date(2020, 3, 4, 0, 0, 0, 0, time.UTC)
	season.Episodes[0].AirDate = &aired
	prepared := creationTV(55101, season)
	prepared.Media.Title = "Trusted Native Series"
	year := int32(2024)
	prepared.Media.Year = &year
	receipt := base.create(t, in, prepared, "automatic-tv-submission")
	execIntent(t, pool, `UPDATE library_root SET health='online',baseline_completed=true WHERE id=$1`, base.tv)
	f := automaticTVFixture{creationFixture: base, repo: postgres.NewAutomaticTVRoundStore(pool, idgen.New()), execution: postgres.NewSearchExecutionStore(pool, codec), indexer: indexer, client: client, sub: receipt.Subscription.ID, unit: receipt.Wanted[0].UnitID, wanted: receipt.Wanted[0].ID}
	if explicit {
		tx, e := pool.Begin(t.Context())
		if e != nil {
			t.Fatal(e)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		q := db.New(tx)
		sub, e := q.GetSubscriptionByID(t.Context(), f.sub)
		if e != nil {
			t.Fatal(e)
		}
		m, e := q.GetMediaByID(t.Context(), sub.MediaID)
		if e != nil {
			t.Fatal(e)
		}
		inputs, _ := json.Marshal([]any{sub.ID, sub.IntentVersion, m.ID, m.MetadataVersion, "standard"})
		digest := sha256.Sum256(inputs)
		source := sha256.Sum256([]byte("{}"))
		row, e := q.InsertEpisodeMappingRevision(t.Context(), db.InsertEpisodeMappingRevisionParams{SubscriptionID: sub.ID, Revision: 1, MediaID: m.ID, MediaVersion: m.MetadataVersion, IntentVersion: sub.IntentVersion + 1, Mode: "standard", InputStamp: fmt.Sprintf("%x", digest), SourceHash: fmt.Sprintf("%x", source), SourceSnapshot: []byte("{}"), Mappings: []byte("[]")})
		if e != nil {
			t.Fatal(e)
		}
		changed, e := q.AdvanceEpisodeMappingBinding(t.Context(), db.AdvanceEpisodeMappingBindingParams{ID: sub.ID, EpisodeMappingRevision: pgtype.Int8{Int64: row.Revision, Valid: true}, IntentVersion: sub.IntentVersion, EpisodeMappingRevision_2: pgtype.Int8{Int64: 0, Valid: true}})
		if e != nil {
			t.Fatal(e)
		}
		if e = q.InvalidateEpisodeMappingWanted(t.Context(), db.InvalidateEpisodeMappingWantedParams{SubscriptionID: sub.ID, IntentVersion: changed.IntentVersion}); e != nil {
			t.Fatal(e)
		}
		if e = tx.Commit(t.Context()); e != nil {
			t.Fatal(e)
		}
	}
	return f
}

func newAutomaticTVSubmissionFixture(t *testing.T, explicit bool, noHash ...bool) automaticTVSubmissionFixture {
	t.Helper()
	f := automaticTVSubmissionBase(t, explicit)
	proof := automaticTVSubmissionMetainfo(t, "Trusted.Native.Series.2024.S02E03.mkv")
	r := f.start(t)
	c := tvExecutionCandidate(t, f, "submission", "1080p", 5)
	hash := proof.InfoHash()
	c.InfoHash = &hash
	if len(noHash) > 0 && noHash[0] {
		c.InfoHash = nil
	}
	tvExecutionPage(t, f, r, c)
	finalizer := tvExecutionLease(t, f, r)
	d, e := tvExecutionStore(f, acquisition.SelectAutomaticTVEpisode).FinalizeAutomaticTVRound(t.Context(), r.ID, finalizer)
	if e != nil || d.State != "reserved" {
		t.Fatalf("actual88 finalizer %v %+v", e, d)
	}
	j, e := db.New(f.pool).ClaimJob(t.Context(), db.ClaimJobParams{Column1: []string{"automatic_tv_download_submission"}, Column2: "original-sender", Column3: 60000})
	if e != nil {
		t.Fatal(e)
	}
	req := ports.AutomaticTVSendRequest{OperationID: d.OperationID, Lease: ports.JobLease{ID: j.ID, Owner: "original-sender", Token: j.LeaseToken}}
	store := postgres.NewAutomaticTVSubmissionStore(f.pool, idgen.New(), crypto.NewSecretProtector(bytes.Repeat([]byte{27}, 32)))
	return automaticTVSubmissionFixture{automaticTVFixture: f, store: store, request: req, command: ports.PrepareAutomaticTVSubmissionCommand{Request: req, TaskID: uuid.New(), Metainfo: proof}}
}

func (f *automaticTVSubmissionFixture) attempt(t *testing.T, corrupt ...string) {
	t.Helper()
	conn, e := f.pool.Acquire(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	digest := sha256.Sum256([]byte("pulsemedia.automatic-tv.physical.v1:" + f.request.OperationID.String()))
	key := int64(binary.BigEndian.Uint64(digest[:8]))
	var held bool
	if e = conn.QueryRow(t.Context(), `SELECT pg_try_advisory_lock($1)`, key).Scan(&held); e != nil || !held {
		t.Fatal("physical gate", held, e)
	}
	f.command.PhysicalAttemptID = uuid.New()
	f.physical = conn
	pid, recordKey, owner, token := int32(conn.Conn().PgConn().PID()), key, f.request.Lease.Owner, f.request.Lease.Token
	if len(corrupt) > 0 {
		switch corrupt[0] {
		case "pid":
			pid += 1000000
		case "key":
			recordKey++
		case "owner":
			owner += "-foreign"
		case "token":
			token++
		}
	}
	execIntent(t, f.pool, `INSERT INTO automatic_tv_physical_attempt(id,operation_id,job_id,lease_owner,lease_token,backend_pid,advisory_key) VALUES($1,$2,$3,$4,$5,$6,$7)`, f.command.PhysicalAttemptID, f.request.OperationID, f.request.Lease.ID, owner, token, pid, recordKey)
	t.Cleanup(func() {
		if f.physical != nil {
			_, _ = f.physical.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, key)
			f.physical.Release()
			f.physical = nil
		}
	})
}
func (f *automaticTVSubmissionFixture) prepare(t *testing.T) {
	t.Helper()
	if f.command.PhysicalAttemptID == uuid.Nil {
		f.attempt(t)
	}
	got, e := f.store.PrepareAutomaticTVSubmission(t.Context(), f.command)
	if e != nil || got.TaskID != f.command.TaskID || got.State != "prepared_not_sent" || got.AlreadyExisted {
		t.Fatalf("prepare %+v %v", got, e)
	}
}
func (f automaticTVSubmissionFixture) result(ack bool, add error) ports.AutomaticTVSubmissionResultCommand {
	return ports.AutomaticTVSubmissionResultCommand{Request: f.request, TaskID: f.command.TaskID, PhysicalAttemptID: f.command.PhysicalAttemptID, Metainfo: f.command.Metainfo, Acknowledged: ack, AddError: add}
}
func automaticTVSubmissionUnchanged(t *testing.T, f automaticTVSubmissionFixture, before string) {
	t.Helper()
	if got := tvExecutionState(t, f.automaticTVFixture); got != before {
		t.Fatal("original authority, protections, job or audit changed on rejection")
	}
	if n := intentCount(t, f.pool, `SELECT count(*) FROM download_task`); n != 0 {
		t.Fatal("rejected prepare created task", n)
	}
	if n := intentCount(t, f.pool, `SELECT count(*) FROM automatic_tv_submission_preparation`); n != 0 {
		t.Fatal("rejected prepare created evidence", n)
	}
}

func TestAutomaticTVSubmissionLoadAndPreparePG(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint("standard_revision_", explicit), func(t *testing.T) {
			f := newAutomaticTVSubmissionFixture(t, explicit)
			execIntent(t, f.pool, `UPDATE download_client SET remote_path_map=jsonb_build_object($2::text,'/mapped/downloads') WHERE id=$1`, f.client, f.path)
			// Changing a client after authority must fail; restore its exact bytes before success.
			if _, e := f.store.LoadAutomaticTVSendWork(t.Context(), f.request); !errors.Is(e, ports.ErrAutomaticTVSubmissionReview) {
				t.Fatal("changed pathmap", e)
			}
			execIntent(t, f.pool, `UPDATE download_client SET remote_path_map='{}' WHERE id=$1`, f.client)
			w, e := f.store.LoadAutomaticTVSendWork(t.Context(), f.request)
			if e != nil {
				t.Fatal("load", e)
			}
			if w.Authority.OperationID != f.request.OperationID || w.RootPath != f.path+"/tv" || w.SavePath != w.RootPath || !strings.Contains(w.SourceEndpointURL, "apikey=private-query-key") || len(w.DownloadURLEncrypted) == 0 {
				t.Fatal("work binding")
			}
			raw, _ := json.Marshal(w)
			if string(raw) != "{}" {
				t.Fatal("transient secrets serialized", string(raw))
			}
			w.DownloadURLEncrypted[0] ^= 1
			if _, e = f.store.LoadAutomaticTVSendWork(t.Context(), f.request); e != nil {
				t.Fatal("returned ciphertext aliased", e)
			}
			f.prepare(t)
			if _, e = f.store.LoadAutomaticTVSendWork(t.Context(), f.request); !errors.Is(e, ports.ErrAutomaticTVNeedsObservation) {
				t.Fatal("prepared task reloaded for Add", e)
			}
			if _, e = f.store.PrepareAutomaticTVSubmission(t.Context(), f.command); !errors.Is(e, ports.ErrAutomaticTVNeedsObservation) {
				t.Fatal("duplicate first-send permission", e)
			}
			if n := intentCount(t, f.pool, `SELECT count(*) FROM download_task WHERE state='submitting'`); n != 1 {
				t.Fatal(n)
			}
			if n := intentCount(t, f.pool, `SELECT count(*) FROM unit_fact`); n != 0 {
				t.Fatal("prepare invented facts", n)
			}
		})
	}
}

func TestAutomaticTVSubmissionRejectChangedFactsPG(t *testing.T) {
	cases := []struct{ name, sql string }{
		{"owner_disabled", `UPDATE app_user SET is_active=false`},
		{"intent", `UPDATE subscription SET intent_version=intent_version+1`},
		{"paused", `UPDATE subscription SET state='paused'`},
		{"upgrade", `UPDATE subscription SET upgrade_mode='stop_at_cutoff'`},
		{"policy", `UPDATE policy SET current_revision=current_revision+1`},
		{"unit_metadata", `UPDATE media_unit SET metadata_version=metadata_version+1`},
		{"unit_retired", `UPDATE media_unit SET retired=true`},
		{"unaired", `UPDATE media_unit SET air_date=CURRENT_DATE+1`},
		{"media_title", `UPDATE media SET title='Another Series'`},
		{"root_health", `UPDATE library_root SET health='offline'`},
		{"root_health_revision", `UPDATE library_root SET health_revision=health_revision+1`},
		{"root_generation", `UPDATE library_root SET generation=generation+1`},
		{"root_path_same_generation", `UPDATE library_root SET path=path||'/replacement'`},
		{"client_configuration", `UPDATE download_client SET username='changed'`},
		{"source_offline", `UPDATE indexer SET health='offline'`},
		{"source_disabled", `UPDATE indexer_lifecycle SET enabled=false`},
		{"source_version", `UPDATE indexer SET config_version=config_version+1`},
		{"candidate_title", `UPDATE release_candidate SET title='Wrong.Series.2024.S02E03.1080p.WEB-DL.H264'`},
		{"candidate_cipher", `UPDATE release_candidate SET link_encrypted=decode('abcd','hex')`},
		{"candidate_hash", `UPDATE release_candidate SET info_hash=repeat('f',40)`},
		{"job_token", `UPDATE job SET lease_token = lease_token + 1 WHERE kind='automatic_tv_download_submission'`},
		{"job_payload_extra", `UPDATE job SET payload=payload||'{"extra":true}'::jsonb WHERE kind='automatic_tv_download_submission'`},
		{"job_expired", `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE kind='automatic_tv_download_submission'`},
		{"wanted_skipped", `UPDATE wanted_item SET user_skipped=true,is_skipped=true`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAutomaticTVSubmissionFixture(t, false)
			f.attempt(t)
			execIntent(t, f.pool, tc.sql)
			before := tvExecutionState(t, f.automaticTVFixture)
			if _, e := f.store.LoadAutomaticTVSendWork(t.Context(), f.request); e == nil {
				t.Fatal("changed facts loaded")
			}
			if _, e := f.store.PrepareAutomaticTVSubmission(t.Context(), f.command); e == nil {
				t.Fatal("changed facts prepared")
			}
			automaticTVSubmissionUnchanged(t, f, before)
		})
	}
}

func TestAutomaticTVSubmissionNativeProofAndDecoderPG(t *testing.T) {
	f := newAutomaticTVSubmissionFixture(t, false, true)
	for _, decoder := range []ports.CredentialDecoder{nil, crypto.NewSecretProtector(bytes.Repeat([]byte{99}, 32))} {
		if _, e := postgres.NewAutomaticTVSubmissionStore(f.pool, idgen.New(), decoder).LoadAutomaticTVSendWork(t.Context(), f.request); !errors.Is(e, ports.ErrUnavailable) {
			t.Fatal("invalid decoder", e)
		}
	}
	f.attempt(t)
	names := []string{"Trusted.Native.Series.2024.S02E04.mkv", "Other.Series.2024.S02E03.mkv", "Trusted.Native.Series.2024.S02E03.Sample.mkv", "Trusted.Native.Series.2024.S02E03-E04.mkv", "Trusted.Native.Series.2024.S02E03.Part1.mkv"}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			cmd := f.command
			cmd.Metainfo = automaticTVSubmissionMetainfo(t, name)
			if _, e := f.store.PrepareAutomaticTVSubmission(t.Context(), cmd); !errors.Is(e, ports.ErrAutomaticTVSubmissionReview) {
				t.Fatal("wrong proof", e)
			}
		})
	}
	for _, p := range []*ports.AutomaticTVMetainfoProof{nil, {}} {
		cmd := f.command
		cmd.Metainfo = p
		if _, e := f.store.PrepareAutomaticTVSubmission(t.Context(), cmd); !errors.Is(e, ports.ErrAutomaticTVSubmissionReview) {
			t.Fatal("unsealed proof", e)
		}
	}
	if n := intentCount(t, f.pool, `SELECT count(*) FROM download_task`); n != 0 {
		t.Fatal(n)
	}
}

func TestAutomaticTVSubmissionPhysicalGatePG(t *testing.T) {
	f := newAutomaticTVSubmissionFixture(t, false)
	f.command.PhysicalAttemptID = uuid.New()
	if _, e := f.store.PrepareAutomaticTVSubmission(t.Context(), f.command); !errors.Is(e, ports.ErrAutomaticTVSubmissionReview) {
		t.Fatal("missing nonce", e)
	}
	f.attempt(t)
	wrong := f.command
	wrong.PhysicalAttemptID = uuid.New()
	if _, e := f.store.PrepareAutomaticTVSubmission(t.Context(), wrong); !errors.Is(e, ports.ErrAutomaticTVNeedsObservation) {
		t.Fatal("wrong nonce", e)
	}
	// A lost PG session keeps the persistent attempt active. It can never grant another Add.
	if e := f.physical.Conn().Close(t.Context()); e != nil {
		t.Fatal(e)
	}
	f.physical.Release()
	f.physical = nil
	if _, e := f.store.PrepareAutomaticTVSubmission(t.Context(), f.command); !errors.Is(e, ports.ErrAutomaticTVSubmissionReview) {
		t.Fatal("lost session", e)
	}
	if _, e := f.store.LoadAutomaticTVSendWork(t.Context(), f.request); !errors.Is(e, ports.ErrAutomaticTVNeedsObservation) {
		t.Fatal("uncertain physical attempt reloaded", e)
	}
	if n := intentCount(t, f.pool, `SELECT count(*) FROM automatic_tv_physical_attempt WHERE finished_at IS NULL`); n != 1 {
		t.Fatal(n)
	}
	tvExecutionCounts(t, f.automaticTVFixture, 1)
}

func TestAutomaticTVSubmissionConcurrentPreparePG(t *testing.T) {
	f := newAutomaticTVSubmissionFixture(t, false)
	f.attempt(t)
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for range 6 {
		wg.Go(func() {
			c := f.command
			c.TaskID = uuid.New()
			_, e := f.store.PrepareAutomaticTVSubmission(t.Context(), c)
			errs <- e
		})
	}
	wg.Wait()
	close(errs)
	n := 0
	for e := range errs {
		if e == nil {
			n++
		} else if !errors.Is(e, ports.ErrAutomaticTVNeedsObservation) {
			t.Fatal(e)
		}
	}
	if n != 1 || intentCount(t, f.pool, `SELECT count(*) FROM download_task`) != 1 || intentCount(t, f.pool, `SELECT count(*) FROM automatic_tv_submission_preparation`) != 1 {
		t.Fatal("multiple first sends", n)
	}
}

func TestAutomaticTVSubmissionResultObservationPG(t *testing.T) {
	cases := []struct {
		name        string
		ack         bool
		add         error
		state, task string
	}{
		{"affirmative", true, nil, "tracking", "tracking"},
		{"missing_ack", false, nil, "submission_unknown", "unknown"},
		{"timeout", false, context.DeadlineExceeded, "submission_unknown", "unknown"},
		{"canceled_even_with_ack", true, context.Canceled, "submission_unknown", "unknown"},
		{"unsafe_error", true, errors.New("https://secret.test/?passkey=never-persist"), "submission_unknown", "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAutomaticTVSubmissionFixture(t, false)
			f.prepare(t)
			// Mid-Add permission/configuration changes do not erase the original physical result.
			execIntent(t, f.pool, `UPDATE app_user SET is_active=false`)
			execIntent(t, f.pool, `UPDATE subscription SET state='paused',intent_version=intent_version+1`)
			execIntent(t, f.pool, `UPDATE indexer SET health='offline',config_version=config_version+1`)
			execIntent(t, f.pool, `UPDATE download_client SET username='after-Add'`)
			got, e := f.store.RecordAutomaticTVSubmissionResult(t.Context(), f.result(tc.ack, tc.add))
			if e != nil || got.State != tc.state {
				t.Fatalf("observe %+v %v", got, e)
			}
			if n := intentCount(t, f.pool, `SELECT count(*) FROM download_task WHERE state=$1`, tc.task); n != 1 {
				t.Fatal(n)
			}
			if n := intentCount(t, f.pool, `SELECT count(*) FROM job WHERE id=$1 AND state='succeeded'`, f.request.Lease.ID); n != 1 {
				t.Fatal(n)
			}
			if n := intentCount(t, f.pool, `SELECT count(*) FROM automatic_tv_submission_result WHERE state=$1`, tc.state); n != 1 {
				t.Fatal(n)
			}
			if _, e = f.store.RecordAutomaticTVSubmissionResult(t.Context(), f.result(tc.ack, nil)); !errors.Is(e, ports.ErrAutomaticTVResultStale) {
				t.Fatal("duplicate result", e)
			}
			if n := intentCount(t, f.pool, `SELECT count(*) FROM unit_fact`); n != 0 {
				t.Fatal("observation invented import", n)
			}
			if n := intentCount(t, f.pool, `SELECT count(*) FROM audit_log WHERE payload::text LIKE '%never-persist%'`); n != 0 {
				t.Fatal("raw Add error leaked")
			}
			tvExecutionCounts(t, f.automaticTVFixture, 1)
		})
	}
}

func TestAutomaticTVSubmissionResultLostSessionPG(t *testing.T) {
	f := newAutomaticTVSubmissionFixture(t, false)
	f.prepare(t)
	if e := f.physical.Conn().Close(t.Context()); e != nil {
		t.Fatal(e)
	}
	f.physical.Release()
	f.physical = nil
	got, e := f.store.RecordAutomaticTVSubmissionResult(t.Context(), f.result(true, nil))
	if e != nil || got.State != "tracking" {
		t.Fatal("original return lost session", got, e)
	}
}

func TestAutomaticTVSubmissionResultStalePG(t *testing.T) {
	for _, change := range []string{"token", "expired", "wrong_nonce", "wrong_bytes", "finished_attempt", "fence"} {
		t.Run(change, func(t *testing.T) {
			f := newAutomaticTVSubmissionFixture(t, false)
			f.prepare(t)
			c := f.result(true, nil)
			switch change {
			case "token":
				execIntent(t, f.pool, `UPDATE job SET lease_token = lease_token + 1 WHERE id=$1`, f.request.Lease.ID)
			case "expired":
				execIntent(t, f.pool, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, f.request.Lease.ID)
			case "wrong_nonce":
				c.PhysicalAttemptID = uuid.New()
			case "wrong_bytes":
				c.Metainfo = automaticTVSubmissionMetainfo(t, "Trusted.Native.Series.2024.S02E04.mkv")
			case "finished_attempt":
				execIntent(t, f.pool, `UPDATE automatic_tv_physical_attempt SET finished_at=clock_timestamp() WHERE id=$1`, f.command.PhysicalAttemptID)
			case "fence":
				execIntent(t, f.pool, `UPDATE download_requirement SET submission_fence=submission_fence||'{"protected_digest":"bad"}'::jsonb`)
			}
			if _, e := f.store.RecordAutomaticTVSubmissionResult(t.Context(), c); !errors.Is(e, ports.ErrAutomaticTVResultStale) {
				t.Fatal("late result accepted", e)
			}
			if n := intentCount(t, f.pool, `SELECT count(*) FROM download_task WHERE state='submitting'`); n != 1 {
				t.Fatal("stale callback erased protection", n)
			}
			if n := intentCount(t, f.pool, `SELECT count(*) FROM automatic_tv_submission_result`); n != 0 {
				t.Fatal(n)
			}
		})
	}
}

func TestAutomaticTVSubmissionTailRollbackPG(t *testing.T) {
	for _, phase := range []string{"prepare_audit_error", "prepare_outbox_error", "prepare_expiry", "result_audit_error", "result_outbox_error", "result_expiry", "finish_expiry", "finish_token", "finish_payload"} {
		t.Run(phase, func(t *testing.T) {
			f := newAutomaticTVSubmissionFixture(t, false)
			f.attempt(t)
			isResult := strings.HasPrefix(phase, "result") || strings.HasPrefix(phase, "finish")
			if isResult {
				f.prepare(t)
			}
			action := "automatic_tv.submission_prepared"
			if isResult {
				action = "automatic_tv.submission_observed"
			}
			table, condition := "audit_log", "NEW.action='"+action+"'"
			if strings.Contains(phase, "outbox") {
				table = "outbox_event"
				condition = "NEW.event_type='" + action + "'"
			}
			if strings.HasPrefix(phase, "finish") {
				table = "job"
				condition = "NEW.kind='automatic_tv_download_submission' AND NEW.state='succeeded'"
			}
			body := "RAISE EXCEPTION 'tail fault';"
			if strings.Contains(phase, "expiry") {
				body = "PERFORM pg_sleep(0.12);"
				execIntent(t, f.pool, `UPDATE job SET lease_until=clock_timestamp()+interval '90 milliseconds' WHERE id=$1`, f.request.Lease.ID)
			}
			timing := "AFTER"
			if phase == "finish_token" {
				timing = "BEFORE"
				body = "NEW.lease_token := NEW.lease_token + 1;"
			}
			if phase == "finish_payload" {
				timing = "BEFORE"
				body = "NEW.payload := NEW.payload||'{\"extra\":true}'::jsonb;"
			}
			execIntent(t, f.pool, `CREATE FUNCTION automatic_tv_submission_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF `+condition+` THEN `+body+` END IF; RETURN NEW; END $$`)
			execIntent(t, f.pool, `CREATE TRIGGER automatic_tv_submission_fault `+timing+` INSERT OR UPDATE ON `+table+` FOR EACH ROW EXECUTE FUNCTION automatic_tv_submission_fault()`)
			before := tvExecutionState(t, f.automaticTVFixture)
			if isResult {
				if _, e := f.store.RecordAutomaticTVSubmissionResult(t.Context(), f.result(true, nil)); e == nil {
					t.Fatal("result ignored tail fault")
				}
				if n := intentCount(t, f.pool, `SELECT count(*) FROM automatic_tv_submission_result`); n != 0 {
					t.Fatal(n)
				}
				if n := intentCount(t, f.pool, `SELECT count(*) FROM download_task WHERE state='submitting'`); n != 1 {
					t.Fatal(n)
				}
			} else {
				if _, e := f.store.PrepareAutomaticTVSubmission(t.Context(), f.command); e == nil {
					t.Fatal("prepare ignored tail fault")
				}
				automaticTVSubmissionUnchanged(t, f, before)
			}
			if got := tvExecutionState(t, f.automaticTVFixture); got != before {
				t.Fatal("tail failure changed original protections or job")
			}
		})
	}
}

func TestAutomaticTVSubmissionHistoryGuardsPG(t *testing.T) {
	f := newAutomaticTVSubmissionFixture(t, false)
	f.prepare(t)
	if _, e := f.store.RecordAutomaticTVSubmissionResult(t.Context(), f.result(false, context.DeadlineExceeded)); e != nil {
		t.Fatal(e)
	}
	for _, table := range []string{"automatic_tv_submission_preparation", "automatic_tv_submission_result", "automatic_tv_physical_attempt"} {
		for _, statement := range []string{"DELETE FROM " + table, "TRUNCATE " + table, "UPDATE " + table + " SET operation_id=operation_id"} {
			if _, e := f.pool.Exec(t.Context(), statement); e == nil {
				t.Fatal("history mutation allowed", statement)
			}
		}
	}
	execIntent(t, f.pool, `UPDATE automatic_tv_physical_attempt SET finished_at=clock_timestamp()`)
	if _, e := f.pool.Exec(t.Context(), `UPDATE automatic_tv_physical_attempt SET finished_at=clock_timestamp()`); e == nil {
		t.Fatal("finished attempt reused")
	}
}

// A per-test SQL clock changes only these new connections; historical authority
// remains the actual immutable finalizer output, including the five-minute TTL.
func automaticTVSubmissionFutureClock(t *testing.T, f automaticTVSubmissionFixture) *postgres.AutomaticTVSubmissionStore {
	t.Helper()
	execIntent(t, f.pool, `UPDATE job SET lease_until=clock_timestamp()+interval '1 day' WHERE id=$1`, f.request.Lease.ID)
	execIntent(t, f.pool, `CREATE FUNCTION public.clock_timestamp() RETURNS timestamptz LANGUAGE SQL VOLATILE RETURN pg_catalog.clock_timestamp()+interval '6 minutes'`)
	cfg := f.pool.Config()
	cfg.ConnConfig.RuntimeParams["search_path"] = "public,pg_catalog"
	p, e := pgxpool.NewWithConfig(t.Context(), cfg)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(p.Close)
	return postgres.NewAutomaticTVSubmissionStore(p, idgen.New(), crypto.NewSecretProtector(bytes.Repeat([]byte{27}, 32)))
}

func TestAutomaticTVSubmissionFirstSendDeadlineVsObservationPG(t *testing.T) {
	t.Run("expired_first_send", func(t *testing.T) {
		f := newAutomaticTVSubmissionFixture(t, false)
		f.attempt(t)
		future := automaticTVSubmissionFutureClock(t, f)
		before := tvExecutionState(t, f.automaticTVFixture)
		if _, e := future.PrepareAutomaticTVSubmission(t.Context(), f.command); !errors.Is(e, ports.ErrAutomaticTVSubmissionReview) {
			t.Fatal("expired first send", e)
		}
		automaticTVSubmissionUnchanged(t, f, before)
	})
	t.Run("expired_after_original_send", func(t *testing.T) {
		f := newAutomaticTVSubmissionFixture(t, false)
		f.prepare(t)
		future := automaticTVSubmissionFutureClock(t, f)
		got, e := future.RecordAutomaticTVSubmissionResult(t.Context(), f.result(true, nil))
		if e != nil || got.State != "tracking" {
			t.Fatal("expired send window erased original result", got, e)
		}
	})
}

func TestAutomaticTVSubmissionFullRawProofBindingPG(t *testing.T) {
	f := newAutomaticTVSubmissionFixture(t, false)
	f.prepare(t)
	original := f.command.Metainfo.Bytes()
	// The announcement bytes are outside info: infohash is unchanged, full SHA differs.
	changed := []byte("d8:announce18:https://other.test" + string(original[1:]))
	p, e := ports.InspectAutomaticTVMetainfo(torrent.Inspector{}, changed)
	if e != nil {
		t.Fatal(e)
	}
	if p.InfoHash() != f.command.Metainfo.InfoHash() || p.SHA256() == f.command.Metainfo.SHA256() {
		t.Fatal("invalid byte-envelope control")
	}
	c := f.result(true, nil)
	c.Metainfo = p
	if _, e = f.store.RecordAutomaticTVSubmissionResult(t.Context(), c); !errors.Is(e, ports.ErrAutomaticTVResultStale) {
		t.Fatal("accepted alternate raw bytes", e)
	}
}

func TestAutomaticTVSubmissionMigration89PG(t *testing.T) {
	t.Run("no_history_up_down_up", func(t *testing.T) {
		f := automaticTVSubmissionBase(t, false)
		dsn := f.pool.Config().ConnString()
		if v, e := postgres.Rollback(t.Context(), dsn, filepath.Join(f.path, "migrations89")); e != nil || v != 88 {
			t.Fatal("89 down no history", v, e)
		}
		if v, e := postgres.Migrate(t.Context(), dsn, filepath.Join(f.path, "migrations89")); e != nil || v != 89 {
			t.Fatal("89 up again", v, e)
		}
	})
	t.Run("history_refuses_down", func(t *testing.T) {
		f := newAutomaticTVSubmissionFixture(t, false)
		f.attempt(t)
		if _, e := postgres.Rollback(t.Context(), f.pool.Config().ConnString(), filepath.Join(f.path, "migrations89")); e == nil {
			t.Fatal("89 discarded persistent physical history")
		}
		if n := intentCount(t, f.pool, `SELECT count(*) FROM automatic_tv_physical_attempt`); n != 1 {
			t.Fatal("history lost", n)
		}
	})
}
func TestAutomaticTVSubmissionPhysicalIdentityPG(t *testing.T) {
	for _, change := range []string{"pid", "key", "owner", "token"} {
		t.Run(change, func(t *testing.T) {
			f := newAutomaticTVSubmissionFixture(t, false)
			f.attempt(t, change)
			before := tvExecutionState(t, f.automaticTVFixture)
			if _, e := f.store.PrepareAutomaticTVSubmission(t.Context(), f.command); !errors.Is(e, ports.ErrAutomaticTVSubmissionReview) {
				t.Fatal("mismatched physical identity", e)
			}
			automaticTVSubmissionUnchanged(t, f, before)
		})
	}
}

func automaticTVSubmissionMultiProof(t *testing.T, parent string, names []string, sizes []int64) *ports.AutomaticTVMetainfoProof {
	t.Helper()
	encoded := func(s string) string { return strconv.Itoa(len(s)) + ":" + s }
	files := "l"
	for i, name := range names {
		path := "l"
		for _, part := range strings.Split(name, "/") {
			path += encoded(part)
		}
		path += "e"
		files += "d6:lengthi" + strconv.FormatInt(sizes[i], 10) + "e4:path" + path + "e"
	}
	files += "e"
	raw := []byte("d4:infod5:files" + files + "4:name" + encoded(parent) + "12:piece lengthi16384e6:pieces20:" + strings.Repeat("0", 20) + "ee")
	p, e := ports.InspectAutomaticTVMetainfo(torrent.Inspector{}, raw)
	if e != nil {
		t.Fatal("native multifile inspector", e)
	}
	return p
}

func TestAutomaticTVSubmissionExactVideoFilesPG(t *testing.T) {
	for _, kind := range []string{"sidecar", "extra_video", "wrong_catalog"} {
		t.Run(kind, func(t *testing.T) {
			f := newAutomaticTVSubmissionFixture(t, false, true)
			f.attempt(t)
			parent := "Trusted Native Series"
			names := []string{"Trusted.Native.Series.2024.S02E03.mkv", "Trusted.Native.Series.2024.S02E03.nfo"}
			sizes := []int64{1234, 0}
			if kind == "extra_video" {
				names[1] = "Trusted.Native.Series.2024.S02E04.mkv"
				sizes = []int64{600, 634}
			}
			if kind == "wrong_catalog" {
				parent = "Another Series"
			}
			f.command.Metainfo = automaticTVSubmissionMultiProof(t, parent, names, sizes)
			got, e := f.store.PrepareAutomaticTVSubmission(t.Context(), f.command)
			if kind == "sidecar" {
				if e != nil || got.TaskID != f.command.TaskID {
					t.Fatal("sidecar blocked", got, e)
				}
			} else if !errors.Is(e, ports.ErrAutomaticTVSubmissionReview) {
				t.Fatal("wrong file set accepted", e)
			}
		})
	}
}

func TestAutomaticTVSubmissionCommittedTaskAlwaysObservePG(t *testing.T) {
	f := newAutomaticTVSubmissionFixture(t, false)
	f.prepare(t)
	execIntent(t, f.pool, `UPDATE app_user SET is_active=false`)
	execIntent(t, f.pool, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, f.request.Lease.ID)
	if _, e := f.store.LoadAutomaticTVSendWork(t.Context(), f.request); !errors.Is(e, ports.ErrAutomaticTVNeedsObservation) {
		t.Fatal("committed send lost observation fence", e)
	}
	if _, e := f.store.PrepareAutomaticTVSubmission(t.Context(), f.command); !errors.Is(e, ports.ErrAutomaticTVNeedsObservation) {
		t.Fatal("committed send reauthorized", e)
	}
}
