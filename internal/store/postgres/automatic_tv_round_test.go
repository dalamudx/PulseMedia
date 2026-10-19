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

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/google/uuid"
)

type automaticTVFixture struct {
	creationFixture
	repo                               *postgres.AutomaticTVRoundStore
	execution                          *postgres.SearchExecutionStore
	indexer, client, sub, unit, wanted uuid.UUID
}

func newAutomaticTVFixture(t *testing.T) automaticTVFixture {
	t.Helper()
	f := newCreationFixture(t)
	ctx := t.Context()
	codec := crypto.NewSecretProtector(bytes.Repeat([]byte{27}, 32))
	key, e := codec.EncryptSecret([]byte("fixture-api-key"))
	if e != nil {
		t.Fatal(e)
	}
	indexer, client := uuid.New(), uuid.New()
	url, e := codec.EncryptSecret([]byte("https://tv-source.test/api?apikey=private-query-key"))
	if e != nil {
		t.Fatal(e)
	}
	execIntent(t, f.pool, `INSERT INTO indexer(id,name,url,api_key_encrypted,categories,health) VALUES($1,'native TV','https://tv-source.test',$2,'[5040,5000]','online')`, indexer, key)
	execIntent(t, f.pool, `INSERT INTO indexer_configuration(indexer_id,protocol,url_encrypted,behavior_version,configuration_version) VALUES($1,'torznab',$2,'explicit-v1',1)`, indexer, url)
	execIntent(t, f.pool, `INSERT INTO indexer_lifecycle(indexer_id,enabled) VALUES($1,true)`, indexer)
	execIntent(t, f.pool, `INSERT INTO download_client(id,name,client_type,url,username,password_encrypted,remote_path_map) VALUES($1,'explicit TV client','qbittorrent','https://downloader.test','fixture',$2,'{}')`, client, key)
	in := creationInput(55101, "tv")
	in.IndexerIDs = []uuid.UUID{indexer}
	in.DownloaderID = &client
	season := creationSeason(2, 3)
	aired := time.Date(2020, 3, 4, 0, 0, 0, 0, time.UTC)
	season.Episodes[0].AirDate = &aired
	prepared := creationTV(55101, season)
	prepared.Media.Title = "Trusted Native Series"
	year := int32(2024)
	prepared.Media.Year = &year
	receipt := f.create(t, in, prepared, "automatic-tv-fixture")
	execIntent(t, f.pool, `UPDATE library_root SET health='online',baseline_completed=true WHERE id=$1`, f.tv)
	if e = f.pool.Ping(ctx); e != nil {
		t.Fatal(e)
	}
	return automaticTVFixture{creationFixture: f, repo: postgres.NewAutomaticTVRoundStore(f.pool, idgen.New()), execution: postgres.NewSearchExecutionStore(f.pool, codec), indexer: indexer, client: client, sub: receipt.Subscription.ID, unit: receipt.Wanted[0].UnitID, wanted: receipt.Wanted[0].ID}
}
func (f automaticTVFixture) start(t *testing.T) *ports.AutomaticTVRound {
	t.Helper()
	r, e := f.repo.StartAutomaticTVRound(t.Context(), ports.AutomaticTVRoundStart{WantedID: f.wanted})
	if e != nil {
		t.Fatal("start", e)
	}
	return r
}
func (f automaticTVFixture) load(t *testing.T, r *ports.AutomaticTVRound) *ports.AutomaticTVSourceWork {
	t.Helper()
	job, e := db.New(f.pool).ClaimJob(t.Context(), db.ClaimJobParams{Column1: []string{"automatic_tv_source"}, Column2: "automatic-tv-test", Column3: 60000})
	if e != nil {
		t.Fatal(e)
	}
	task := ports.AutomaticTVSourceTask{RoundID: r.ID, IndexerID: f.indexer, Lease: ports.JobLease{ID: job.ID, Owner: "automatic-tv-test", Token: job.LeaseToken}}
	w, e := f.repo.LoadAutomaticTVSource(t.Context(), task)
	if e != nil {
		t.Fatal("load", e)
	}
	return w
}
func automaticTVSpec(t *testing.T, w *ports.AutomaticTVSourceWork, page int32) ports.SearchQuerySpec {
	t.Helper()
	q := w.Query
	q.Offset = page * 50
	q, _, hash, e := search.CanonicalTVEpisodeQuery(q)
	if e != nil {
		t.Fatal(e)
	}
	return ports.SearchQuerySpec{ID: uuid.New(), IndexerID: w.Task.IndexerID, Keyword: "tv-episode-query-v1:" + hash, ConfigVersion: w.ConfigVersion, Page: page, AutomaticTV: &ports.AutomaticTVQueryBinding{Task: w.Task, SnapshotHash: w.SnapshotHash, Query: q, ParametersHash: hash}}
}
func (f automaticTVFixture) permit(t *testing.T, s ports.SearchQuerySpec) ports.SearchExecutionPermit {
	t.Helper()
	a, p, e := f.execution.AdmitQuery(t.Context(), s)
	if e != nil || p == nil || a.Shared != nil {
		t.Fatalf("admit %v permit=%v shared=%v", e, p != nil, a.Shared != nil)
	}
	if !strings.Contains(a.Indexer.Url, "apikey=private-query-key") {
		t.Fatal("execution did not resolve encrypted full URL")
	}
	t.Cleanup(p.Close)
	if _, e = p.BeginQuery(t.Context()); e != nil {
		t.Fatal("begin", e)
	}
	return p
}
func automaticTVCommit(s ports.SearchQuerySpec, c ...ports.SearchCandidate) ports.SearchQueryCommit {
	total := int64(s.Page)*50 + int64(len(c))
	return ports.SearchQueryCommit{Candidates: c, Page: &ports.SearchPageEvidence{QueryID: s.ID, PageOffset: int64(s.Page) * 50, PageLimit: 50, ItemCount: int32(len(c)), OffsetVerified: true, Total: &total}}
}
func automaticTVCandidate(f automaticTVFixture, guid string) ports.SearchCandidate {
	return ports.SearchCandidate{ID: uuid.New(), IndexerID: f.indexer, SourceConfigVersion: 1, Title: "Trusted.Native.Series.2024.S02E03.1080p.WEB-DL.H264", Guid: guid, LinkEncrypted: []byte("encrypted-fixture-download"), SizeBytes: 1234, Seeders: 5, QualityProfile: []byte(`{"resolution":"1080p"}`)}
}
func automaticTVState(t *testing.T, f automaticTVFixture) string {
	t.Helper()
	var value string
	if e := f.pool.QueryRow(t.Context(), `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM indexer_query t),(SELECT jsonb_agg(to_jsonb(t) ORDER BY query_id) FROM indexer_query_page t),(SELECT jsonb_agg(to_jsonb(t) ORDER BY query_id,candidate_id) FROM indexer_query_candidate t),(SELECT jsonb_agg(to_jsonb(t) ORDER BY query_id,candidate_id) FROM automatic_tv_query_candidate t),(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM release_candidate t),(SELECT jsonb_agg(to_jsonb(t) ORDER BY round_id,indexer_id) FROM automatic_tv_source t),(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM job t))::text`).Scan(&value); e != nil {
		t.Fatal(e)
	}
	return value
}
func TestAutomaticTVRoundSnapshotAndConcurrencyPG(t *testing.T) {
	f := newAutomaticTVFixture(t)
	var wg sync.WaitGroup
	rounds := make(chan *ports.AutomaticTVRound, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			r, e := f.repo.StartAutomaticTVRound(t.Context(), ports.AutomaticTVRoundStart{WantedID: f.wanted})
			rounds <- r
			errs <- e
		})
	}
	wg.Wait()
	close(rounds)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var first *ports.AutomaticTVRound
	for r := range rounds {
		if first == nil {
			first = r
		} else if r.ID != first.ID {
			t.Fatal("duplicate live round")
		}
	}
	snap := first.Snapshot
	if snap.TMDBID != 55101 || snap.EpisodeID != 10203 || snap.Title != "Trusted Native Series" || snap.OriginalTitle != "" || snap.UnitKey != "S02E03" || snap.MappingMode != "standard" || snap.RootHealthRevision < 1 || snap.ClientID != f.client || snap.MediaVersion < 1 || snap.UnitVersion < 1 || first.Deadline.Sub(snap.ObservedAt) != 30*time.Second {
		t.Fatalf("incomplete snapshot %+v", snap)
	}
	if got := intentCount(t, f.pool, `SELECT count(*) FROM automatic_tv_source`); got != 1 {
		t.Fatal(got)
	}
	if got := intentCount(t, f.pool, `SELECT count(*) FROM job WHERE kind IN ('automatic_tv_source','automatic_tv_finalize')`); got != 2 {
		t.Fatal(got)
	}
	if got := intentCount(t, f.pool, `SELECT count(*) FROM search_round`); got != 0 {
		t.Fatal("legacy round reused")
	}
	if got := intentCount(t, f.pool, `SELECT count(*) FROM job j JOIN automatic_tv_round r ON r.finalize_job_id=j.id WHERE j.kind='automatic_tv_finalize' AND j.not_before=r.deadline`); got != 1 {
		t.Fatal("finalizer schedule", got)
	}
	w := f.load(t, first)
	if w.Query.Offset != 0 || w.Query.Limit != 50 || w.Query.Keyword != "Trusted Native Series 2024" || w.NextPage != 0 || len(w.SavedQueries) != 0 {
		t.Fatalf("work %+v", w)
	}
	// A caller modifying its owned copy cannot rewrite durable parameters.
	w.Query.Categories[0] = 999
	again, e := f.repo.LoadAutomaticTVSource(t.Context(), w.Task)
	if e != nil || again.Query.Categories[0] != 5000 {
		t.Fatal("not a clone", e)
	}
}
func TestAutomaticTVStartHoldsPG(t *testing.T) {
	cases := []struct{ name, sql string }{
		{"future", `UPDATE media_unit SET air_date=CURRENT_DATE+1 WHERE id=$1`},
		{"null-air", `UPDATE media_unit SET air_date=NULL WHERE id=$1`},
		{"season-zero", `UPDATE media_unit SET season_number=0 WHERE id=$1`},
		{"absolute", `UPDATE media_unit SET absolute_number=3 WHERE id=$1`},
		{"retired", `UPDATE media_unit SET retired=true WHERE id=$1`},
		{"paused", `UPDATE subscription SET state='paused' WHERE id=$2`},
		{"skipped", `UPDATE wanted_item SET is_skipped=true WHERE id=$3`},
		{"owner-disabled", `UPDATE app_user SET is_active=false WHERE id=$4`},
		{"source-offline", `UPDATE indexer SET health='offline' WHERE id=$5`},
		{"source-disabled", `UPDATE indexer_lifecycle SET enabled=false WHERE indexer_id=$5`},
		{"source-deleted", `UPDATE indexer_lifecycle SET enabled=false,deleted_at=clock_timestamp() WHERE indexer_id=$5`},
		{"root-offline", `UPDATE library_root SET health='offline' WHERE id=$6`},
		{"baseline", `UPDATE library_root SET baseline_completed=false WHERE id=$6`},
		{"unbound-client", `UPDATE subscription SET downloader_id=NULL WHERE id=$2`},
		{"manual-revision", `INSERT INTO episode_mapping_revision(subscription_id,revision,media_id,media_version,intent_version,mode,input_stamp,source_hash,source_snapshot,mappings) SELECT s.id,1,s.media_id,m.metadata_version,s.intent_version+1,'manual',repeat('a',64),repeat('b',64),'{}',jsonb_build_array(jsonb_build_object('source_number',3,'unit_id',u.id,'unit_key',u.unit_key,'unit_version',u.metadata_version)) FROM subscription s JOIN media m ON m.id=s.media_id JOIN media_unit u ON u.id=$1 WHERE s.id=$2; UPDATE subscription SET episode_mapping_revision=1,intent_version=intent_version+1 WHERE id=$2; UPDATE wanted_item SET intent_version=(SELECT intent_version FROM subscription WHERE id=$2) WHERE id=$3`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAutomaticTVFixture(t) // referenced arguments are provided through a typed CTE to avoid unused bind types.
			sql := tc.sql
			values := []any{f.unit, f.sub, f.wanted, uuid.MustParse(f.actor.ID), f.indexer, f.tv}
			for i := 6; i >= 1; i-- {
				sql = strings.ReplaceAll(sql, fmt.Sprintf("$%d", i), fmt.Sprintf("'%s'::uuid", values[i-1]))
			}
			execIntent(t, f.pool, sql)
			before := intentCount(t, f.pool, `SELECT count(*) FROM job`)
			if _, e := f.repo.StartAutomaticTVRound(t.Context(), ports.AutomaticTVRoundStart{WantedID: f.wanted}); e == nil {
				t.Fatal("hold admitted")
			}
			if intentCount(t, f.pool, `SELECT count(*) FROM automatic_tv_round`) != 0 || intentCount(t, f.pool, `SELECT count(*) FROM job`) != before {
				t.Fatal("hold wrote jobs")
			}
		})
	}
}
func TestAutomaticTVUnknownFileAndReservationHoldsPG(t *testing.T) {
	for _, kind := range []string{"unknown-file", "unit-reserved"} {
		t.Run(kind, func(t *testing.T) {
			f := newAutomaticTVFixture(t)
			if kind == "unknown-file" {
				file := uuid.New()
				execIntent(t, f.pool, `INSERT INTO library_file(id,root_id,rel_path,size_bytes,mtime_ns) VALUES($1,$2,'unknown.mkv',1234,1)`, file, f.tv)
				execIntent(t, f.pool, `INSERT INTO library_file_unit(file_id,unit_id) VALUES($1,$2)`, file, f.unit)
			} else {
				candidate := automaticTVCandidate(f, "reserved")
				execIntent(t, f.pool, `INSERT INTO release_candidate(id,indexer_id,title,guid,link_encrypted,size_bytes,source_config_version) VALUES($1,$2,'reserved','reserved','cipher',1,1)`, candidate.ID, f.indexer)
				execIntent(t, f.pool, `INSERT INTO unit_reservation(id,unit_id,root_id,purpose,candidate_id,operation_id,state,expires_at) VALUES($1,$2,$3,'acquire',$4,$5,'reserved',clock_timestamp()-interval '1 day')`, uuid.New(), f.unit, f.tv, candidate.ID, uuid.New())
			}
			if _, e := f.repo.StartAutomaticTVRound(t.Context(), ports.AutomaticTVRoundStart{WantedID: f.wanted}); e == nil {
				t.Fatal("unsafe hold admitted")
			}
		})
	}
}
func TestAutomaticTVStartTailRollbackPG(t *testing.T) {
	f := newAutomaticTVFixture(t)
	execIntent(t, f.pool, `CREATE FUNCTION tv_start_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_type='automatic_tv.started' THEN RAISE EXCEPTION 'tail'; END IF; RETURN NEW; END $$; CREATE TRIGGER tv_start_fault BEFORE INSERT ON outbox_event FOR EACH ROW EXECUTE FUNCTION tv_start_fault()`)
	before := automaticTVState(t, f)
	if _, e := f.repo.StartAutomaticTVRound(t.Context(), ports.AutomaticTVRoundStart{WantedID: f.wanted}); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal(e)
	}
	if before != automaticTVState(t, f) || intentCount(t, f.pool, `SELECT count(*) FROM automatic_tv_round`) != 0 {
		t.Fatal("start tail did not roll back")
	}
}
func TestAutomaticTVQueriesFourPagesAndFrozenCandidatePG(t *testing.T) {
	f := newAutomaticTVFixture(t)
	r := f.start(t)
	w := f.load(t, r)
	var original uuid.UUID
	var firstStamp string
	for page := int32(0); page < 4; page++ {
		spec := automaticTVSpec(t, w, page)
		p := f.permit(t, spec)
		candidate := automaticTVCandidate(f, "same-guid")
		if page > 0 {
			candidate.Title = "Changed source title"
			candidate.QualityProfile = []byte(`{"resolution":"2160p"}`)
		}
		result, e := p.CommitQuery(t.Context(), automaticTVCommit(spec, candidate))
		p.Close()
		if e != nil || result.State != "succeeded" {
			t.Fatal("commit", page, e)
		}
		var stamp string
		var id uuid.UUID
		var profile []byte
		var title string
		var cipher []byte
		var hash *string
		e = f.pool.QueryRow(t.Context(), `SELECT candidate_id,source_stamp,title,info_hash,link_encrypted,quality_profile FROM automatic_tv_query_candidate WHERE query_id=$1`, spec.ID).Scan(&id, &stamp, &title, &hash, &cipher, &profile)
		if e != nil {
			t.Fatal(e)
		}
		info := ""
		if hash != nil {
			info = *hash
		}
		if stamp != quality.AcquisitionSourceStamp(title, info, cipher, profile) {
			t.Fatal("stamp not actual returned row")
		}
		if page == 0 {
			original = id
			firstStamp = stamp
		} else if id != original {
			t.Fatal("proof used proposed candidate ID instead of upsert returned ID")
		}
		loaded, e := f.repo.LoadAutomaticTVSource(t.Context(), w.Task)
		if e != nil || loaded.NextPage != page+1 || len(loaded.SavedQueries) != int(page+1) {
			t.Fatal("page association", e)
		}
	}
	var preserved string
	if e := f.pool.QueryRow(t.Context(), `SELECT source_stamp FROM automatic_tv_query_candidate c JOIN automatic_tv_query q ON q.query_id=c.query_id WHERE q.round_id=$1 AND q.page=0`, r.ID).Scan(&preserved); e != nil || preserved != firstStamp {
		t.Fatal("proof overwritten", e)
	}
	if e := f.repo.FinishAutomaticTVSource(t.Context(), *w, ports.AutomaticTVSourceSucceeded); e != nil {
		t.Fatal("finish original work", e)
	}
	if intentCount(t, f.pool, `SELECT count(*) FROM automatic_tv_query`) != 4 || intentCount(t, f.pool, `SELECT count(*) FROM automatic_tv_round WHERE state='running'`) != 1 {
		t.Fatal("page count or unauthorized finalization")
	}
}
func TestAutomaticTVParameterAndForeignQueryRejectionPG(t *testing.T) {
	f := newAutomaticTVFixture(t)
	w := f.load(t, f.start(t))
	for _, change := range []string{"other-tv", "other-episode", "other-page", "actor", "rss", "interactive", "health", "wrong-hash", "wrong-snapshot"} {
		t.Run(change, func(t *testing.T) {
			s := automaticTVSpec(t, w, 0)
			switch change {
			case "other-tv":
				s.AutomaticTV.Query.SeriesID++
			case "other-episode":
				s.AutomaticTV.Query.Episode++
			case "other-page":
				s.Page = 1
			case "actor":
				s.Actor = f.actor
			case "rss":
				s.RSSJobID = uuid.New()
			case "interactive":
				s.Interactive = &ports.InteractiveSearchSourceLease{}
			case "health":
				s.HealthJob = &ports.IndexerHealthJob{}
			case "wrong-hash":
				s.AutomaticTV.ParametersHash = strings.Repeat("0", 64)
			case "wrong-snapshot":
				s.AutomaticTV.SnapshotHash = strings.Repeat("0", 64)
			}
			if change == "other-tv" || change == "other-episode" {
				q, _, h, e := search.CanonicalTVEpisodeQuery(s.AutomaticTV.Query)
				if e != nil {
					t.Fatal(e)
				}
				s.AutomaticTV.Query = q
				s.AutomaticTV.ParametersHash = h
				s.Keyword = "tv-episode-query-v1:" + h
			}
			before := automaticTVState(t, f)
			_, p, e := f.execution.AdmitQuery(t.Context(), s)
			if p != nil {
				p.Close()
			}
			if e == nil || automaticTVState(t, f) != before {
				t.Fatal("invalid spec accepted or wrote", change, e)
			}
		})
	}
	tx, e := f.pool.Begin(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := db.New(tx)
	s := automaticTVSpec(t, w, 0)
	if _, e = tx.Exec(t.Context(), `INSERT INTO indexer_query(id,indexer_id,query_type,keyword,page,config_version,state) VALUES($1,$2,'search',$3,0,1,'running')`, s.ID, f.indexer, s.Keyword); e != nil {
		t.Fatal(e)
	}
	if e = postgres.LinkAutomaticTVSearchQuery(t.Context(), q, *s.AutomaticTV, s.ID); e == nil {
		t.Fatal("legacy query signed as TV")
	}
}
func TestAutomaticTVLeaseFencesAndRecoveryPG(t *testing.T) {
	for _, begun := range []bool{false, true} {
		t.Run(fmt.Sprint(begun), func(t *testing.T) {
			f := newAutomaticTVFixture(t)
			w := f.load(t, f.start(t))
			var p ports.SearchExecutionPermit
			var spec ports.SearchQuerySpec
			if begun {
				spec = automaticTVSpec(t, w, 0)
				p = f.permit(t, spec)
			}
			execIntent(t, f.pool, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, w.Task.Lease.ID)
			before := automaticTVState(t, f)
			if begun {
				if _, e := p.CommitQuery(t.Context(), automaticTVCommit(spec, automaticTVCandidate(f, "late"))); e == nil {
					t.Fatal("late commit")
				}
				if e := p.FailQuery(t.Context(), ports.SearchFailureTimeout, 0); e == nil {
					t.Fatal("late fail")
				}
			}
			if e := f.repo.FinishAutomaticTVSource(t.Context(), *w, ports.AutomaticTVSourceFailed); e == nil {
				t.Fatal("late finish")
			}
			if before != automaticTVState(t, f) {
				t.Fatal("stale lease changed state")
			}
			n, e := f.repo.RecoverAutomaticTVSources(t.Context(), 10)
			if e != nil || n != 1 {
				t.Fatal("recover", n, e)
			}
			var state string
			var token int64
			if e = f.pool.QueryRow(t.Context(), `SELECT state,lease_token FROM job WHERE id=$1`, w.Task.Lease.ID).Scan(&state, &token); e != nil || token <= w.Task.Lease.Token {
				t.Fatal("token fence", e)
			}
			if !begun && state != "queued" {
				t.Fatal("R0 not requeued")
			}
			if begun {
				if state != "failed" {
					t.Fatal("begun replayed")
				}
				if unlock, e := postgres.AcquireIndexerPermit(t.Context(), f.pool, f.indexer); e == nil {
					unlock()
					t.Fatal("recovery released physical permit")
				}
				if intentCount(t, f.pool, `SELECT count(*) FROM indexer_query WHERE state='running'`) != 1 {
					t.Fatal("unresolved query rewritten")
				}
				p.Close()
			}
		})
	}
}
func TestAutomaticTVQueryFaultRollbackAndTailLeasePG(t *testing.T) {
	for _, stage := range []string{"candidate", "page", "query", "tail-lease"} {
		t.Run(stage, func(t *testing.T) {
			f := newAutomaticTVFixture(t)
			w := f.load(t, f.start(t))
			s := automaticTVSpec(t, w, 0)
			p := f.permit(t, s)
			if stage == "tail-lease" {
				execIntent(t, f.pool, `UPDATE job SET lease_until=clock_timestamp()+interval '150 milliseconds' WHERE id=$1`, w.Task.Lease.ID)
				execIntent(t, f.pool, `CREATE FUNCTION tv_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(0.25); RETURN NEW; END $$; CREATE TRIGGER tv_fault BEFORE UPDATE ON indexer_query FOR EACH ROW EXECUTE FUNCTION tv_fault()`)
			} else {
				table, event := "release_candidate", "INSERT"
				if stage == "page" {
					table = "indexer_query_page"
				}
				if stage == "query" {
					table = "indexer_query"
					event = "UPDATE"
				}
				execIntent(t, f.pool, `CREATE FUNCTION tv_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fault'; END $$; CREATE TRIGGER tv_fault BEFORE `+event+` ON `+table+` FOR EACH ROW EXECUTE FUNCTION tv_fault()`)
			}
			before := automaticTVState(t, f)
			if _, e := p.CommitQuery(t.Context(), automaticTVCommit(s, automaticTVCandidate(f, "rollback"))); e == nil {
				t.Fatal("fault accepted")
			}
			if before != automaticTVState(t, f) {
				t.Fatal("failed callback leaked rows")
			}
		})
	}
}
func TestAutomaticTVHistoryAndDowngradePG(t *testing.T) {
	f := newAutomaticTVFixture(t)
	r := f.start(t)
	w := f.load(t, r)
	s := automaticTVSpec(t, w, 0)
	p := f.permit(t, s)
	if _, e := p.CommitQuery(t.Context(), automaticTVCommit(s, automaticTVCandidate(f, "durable"))); e != nil {
		t.Fatal(e)
	}
	p.Close()
	for _, table := range []string{"automatic_tv_round", "automatic_tv_source", "automatic_tv_query", "automatic_tv_query_candidate"} {
		for _, sql := range []string{"DELETE FROM " + table, "TRUNCATE " + table + " CASCADE"} {
			if _, e := f.pool.Exec(t.Context(), sql); e == nil {
				t.Fatal("history mutable", sql)
			}
		}
	}
	if _, e := f.pool.Exec(t.Context(), `UPDATE indexer_query SET keyword='foreign' WHERE id=$1`, s.ID); e == nil {
		t.Fatal("query proof scope changed")
	}
	var params []byte
	if e := f.pool.QueryRow(t.Context(), `SELECT parameters FROM automatic_tv_query WHERE query_id=$1`, s.ID).Scan(&params); e != nil {
		t.Fatal(e)
	}
	var typed ports.TVEpisodeQuery
	if json.Unmarshal(params, &typed) != nil {
		t.Fatal("typed params")
	}
	_, _, hash, e := search.CanonicalTVEpisodeQuery(typed)
	if e != nil || s.Keyword != "tv-episode-query-v1:"+hash {
		t.Fatal("canonical store/usecase mismatch", e)
	}
	dsn := f.pool.Config().ConnString()
	if _, e = postgres.Rollback(t.Context(), dsn, "../../../migrations"); e == nil {
		t.Fatal("history downgrade allowed")
	}
}
