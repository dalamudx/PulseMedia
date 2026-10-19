package postgres_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
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
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// Arrange a real, explicit standard catalog revision. This does not insert an
// automatic decision or authority; every positive still uses Source87's permit.
func newTVExecutionFixture(t *testing.T, through ...int64) automaticTVFixture {
	t.Helper()
	f := newAutomaticTVFixture(t, through...)
	tx, e := f.pool.Begin(t.Context())
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
	inputs, e := json.Marshal([]any{sub.ID, sub.IntentVersion, m.ID, m.MetadataVersion, "standard"})
	if e != nil {
		t.Fatal(e)
	}
	digest := sha256.Sum256(inputs)
	source := sha256.Sum256([]byte("{}"))
	row, e := q.InsertEpisodeMappingRevision(t.Context(), db.InsertEpisodeMappingRevisionParams{SubscriptionID: sub.ID, Revision: 1, MediaID: m.ID, MediaVersion: m.MetadataVersion, IntentVersion: sub.IntentVersion + 1, Mode: "standard", InputStamp: hex.EncodeToString(digest[:]), SourceHash: hex.EncodeToString(source[:]), SourceSnapshot: []byte("{}"), Mappings: []byte("[]")})
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
	return f
}
func tvExecutionCandidate(t *testing.T, f automaticTVFixture, guid, res string, seed int32) ports.SearchCandidate {
	t.Helper()
	c := automaticTVCandidate(f, guid)
	c.Seeders = seed
	c.Title = "Trusted.Native.Series.2024.S02E03." + res + ".WEB-DL.H264"
	var e error
	c.LinkEncrypted, e = crypto.NewSecretProtector(bytes.Repeat([]byte{27}, 32)).EncryptSecret([]byte("https://tv-source.test/download/" + guid))
	if e != nil {
		t.Fatal(e)
	}
	c.QualityProfile, e = json.Marshal(quality.Profile{Resolution: quality.Resolution(res), ResolutionParserVersion: quality.ResolutionTokenParserVersion, Source: quality.SourceWEBDL, SourceParserVersion: quality.SourceTokenParserVersion, Codec: quality.CodecH264, CodecParserVersion: quality.CodecTokenParserVersion})
	if e != nil {
		t.Fatal(e)
	}
	return c
}

func tvExecutionPage(t *testing.T, f automaticTVFixture, r *ports.AutomaticTVRound, c ...ports.SearchCandidate) *ports.AutomaticTVSourceWork {
	t.Helper()
	w := f.load(t, r)
	spec := automaticTVSpec(t, w, 0)
	p := f.permit(t, spec)
	if _, e := p.CommitQuery(t.Context(), automaticTVCommit(spec, c...)); e != nil {
		t.Fatal("commit original proof", e)
	}
	p.Close()
	if e := f.repo.FinishAutomaticTVSource(t.Context(), *w, ports.AutomaticTVSourceSucceeded); e != nil {
		t.Fatal("finish source", e)
	}
	return w
}

func tvExecutionLease(t *testing.T, f automaticTVFixture, r *ports.AutomaticTVRound) ports.JobLease {
	t.Helper()
	execIntent(t, f.pool, `UPDATE job SET not_before=clock_timestamp() WHERE id=(SELECT finalize_job_id FROM automatic_tv_round WHERE id=$1)`, r.ID)
	job, e := db.New(f.pool).ClaimJob(t.Context(), db.ClaimJobParams{Column1: []string{"automatic_tv_finalize"}, Column2: "tv-finalizer", Column3: 60000})
	if e != nil {
		t.Fatal(e)
	}
	return ports.JobLease{ID: job.ID, Owner: "tv-finalizer", Token: job.LeaseToken}
}
func tvExecutionStore(f automaticTVFixture, selector ports.AutomaticTVSelector) *postgres.AutomaticTVAcquisitionStore {
	return postgres.NewAutomaticTVAcquisitionStore(f.pool, idgen.New(), selector)
}
func tvExecutionState(t *testing.T, f automaticTVFixture) string {
	t.Helper()
	var value string
	if e := f.pool.QueryRow(t.Context(), `SELECT jsonb_build_array(
 (SELECT jsonb_agg(to_jsonb(t) ORDER BY operation_id) FROM automatic_tv_execution_authority t),
 (SELECT jsonb_agg(to_jsonb(t) ORDER BY round_id) FROM automatic_tv_decision t),
 (SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM acquisition_reservation t),
 (SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM unit_reservation t),
 (SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM download_requirement t),
 (SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM job t),
 (SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM automatic_tv_round t),
 (SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM wanted_item t),
 (SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM media_unit t),
 (SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM audit_log t),
 (SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM outbox_event t))::text`).Scan(&value); e != nil {
		t.Fatal(e)
	}
	return value
}
func tvExecutionCounts(t *testing.T, f automaticTVFixture, n int) {
	t.Helper()
	for _, table := range []string{"automatic_tv_execution_authority", "acquisition_reservation", "unit_reservation", "download_requirement"} {
		if got := intentCount(t, f.pool, `SELECT count(*) FROM `+table); got != n {
			t.Fatalf("%s=%d want %d", table, got, n)
		}
	}
	if got := intentCount(t, f.pool, `SELECT count(*) FROM job WHERE kind='automatic_tv_download_submission'`); got != n {
		t.Fatalf("submission jobs=%d", got)
	}
}

func TestAutomaticTVFinalizationSelectionRecoveryPG(t *testing.T) {
	f := newTVExecutionFixture(t)
	r := f.start(t)
	low := tvExecutionCandidate(t, f, "low", "720p", 100)
	high := tvExecutionCandidate(t, f, "high", "1080p", 7)
	tie := tvExecutionCandidate(t, f, "tie", "1080p", 7)
	if strings.Compare(high.ID.String(), tie.ID.String()) > 0 {
		high.ID, tie.ID = tie.ID, high.ID
	}
	seed := tvExecutionCandidate(t, f, "seed", "1080p", 2)
	wrong := tvExecutionCandidate(t, f, "other-series", "2160p", 1000)
	wrong.Title = "Other.Series.2024.S02E03.2160p.WEB-DL"
	tvExecutionPage(t, f, r, low, seed, tie, wrong, high)
	lease := tvExecutionLease(t, f, r)
	repo := tvExecutionStore(f, acquisition.SelectAutomaticTVEpisode)
	got, e := repo.FinalizeAutomaticTVRound(t.Context(), r.ID, lease)
	if e != nil {
		t.Fatal("finalize", e)
	}
	if got.State != "reserved" || got.Reason != "selected" || got.SelectedCandidateID == nil || *got.SelectedCandidateID != high.ID || got.BestScore <= 0 || got.OperationID == uuid.Nil {
		t.Fatalf("decision %+v", got)
	}
	tvExecutionCounts(t, f, 1)
	var raw []byte
	if e = f.pool.QueryRow(t.Context(), `SELECT snapshot FROM automatic_tv_execution_authority WHERE operation_id=$1`, got.OperationID).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var a ports.AutomaticTVExecutionAuthority
	if e = json.Unmarshal(raw, &a); e != nil {
		t.Fatal(e)
	}
	if a.SchemaVersion != "automatic-tv-execution-v1" || a.Kind != "automatic_standard_single_episode_v1" || a.Target.OwnerID != uuid.MustParse(f.actor.ID) || a.RoundID != r.ID || a.RoundSnapshotHash != r.SnapshotHash || a.Target.ClientID != f.client || a.CandidateID != high.ID || a.SendValidUntil.Sub(a.IssuedAt) != 5*time.Minute {
		t.Fatalf("authority %+v", a)
	}
	var rootPath string
	if e = f.pool.QueryRow(t.Context(), `SELECT path FROM library_root WHERE id=$1`, f.tv).Scan(&rootPath); e != nil {
		t.Fatal(e)
	}
	pathDigest := sha256.Sum256([]byte(rootPath))
	if a.RootPathDigest != hex.EncodeToString(pathDigest[:]) || bytes.Contains(raw, []byte(rootPath)) {
		t.Fatal("root path was not frozen as a digest")
	}
	stamp := a.DecisionStamp
	a.DecisionStamp = ""
	var rules any
	decoder := json.NewDecoder(strings.NewReader(string(a.Target.Rules)))
	decoder.UseNumber()
	if e = decoder.Decode(&rules); e != nil {
		t.Fatal(e)
	}
	a.Target.Rules, e = json.Marshal(rules)
	if e != nil {
		t.Fatal(e)
	}
	canonical, e := json.Marshal(a)
	if e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(canonical)
	if hex.EncodeToString(sum[:]) != stamp {
		t.Fatal("authority digest is not canonical typed JSON")
	}
	if n := intentCount(t, f.pool, `SELECT count(*) FROM download_requirement WHERE actor_type='system' AND actor_id=$1 AND state='pending' AND submission_fence->>'goal'='acquire' AND submission_fence->>'kind'='automatic_tv_episode_v1' AND submission_fence->>'authority'=$2 AND submission_fence->>'protected_digest'=$3`, r.ID.String(), got.OperationID.String(), stamp); n != 1 {
		t.Fatal("missing automatic fence")
	}
	if n := intentCount(t, f.pool, `SELECT count(*) FROM job WHERE kind='automatic_tv_download_submission' AND payload=jsonb_build_object('operation_id',$1::uuid,'round_id',$2::uuid,'automatic_tv_authority',$1::uuid,'candidate_id',$3::uuid,'root_id',$4::uuid,'client_id',$5::uuid)`, got.OperationID, r.ID, high.ID, f.tv, f.client); n != 1 {
		t.Fatal("payload scope")
	}
	before := tvExecutionState(t, f)
	again, e := repo.FinalizeAutomaticTVRound(t.Context(), r.ID, lease)
	if e != nil || !reflect.DeepEqual(got, again) || before != tvExecutionState(t, f) {
		t.Fatal("unstable recovery", e)
	}
	for _, foreign := range []ports.JobLease{{ID: lease.ID, Owner: "other", Token: lease.Token}, {ID: lease.ID, Owner: lease.Owner, Token: lease.Token + 1}, {ID: uuid.New(), Owner: lease.Owner, Token: lease.Token}} {
		if _, e = repo.FinalizeAutomaticTVRound(t.Context(), r.ID, foreign); !errors.Is(e, ports.ErrAcquisitionRoundAborted) {
			t.Fatal("foreign recovery", e)
		}
	}
	execIntent(t, f.pool, `UPDATE app_user SET is_active=false WHERE id=$1`, uuid.MustParse(f.actor.ID))
	if _, e = repo.FinalizeAutomaticTVRound(t.Context(), r.ID, lease); !errors.Is(e, ports.ErrAcquisitionRoundAborted) {
		t.Fatal("revoked owner recovery", e)
	}
}

func TestAutomaticTVFinalizationCallbackRollbackPG(t *testing.T) {
	for _, kind := range []string{"target", "rules", "source-version", "query", "cipher", "profile", "candidate-id", "fabricated", "blocked", "false-negative", "panic"} {
		t.Run(kind, func(t *testing.T) {
			f := newTVExecutionFixture(t)
			r := f.start(t)
			good := tvExecutionCandidate(t, f, "good", "1080p", 1)
			blocked := tvExecutionCandidate(t, f, "blocked", "2160p", 50)
			blocked.Title = "Unrelated.2024.S02E03.2160p.WEB-DL"
			tvExecutionPage(t, f, r, good, blocked)
			lease := tvExecutionLease(t, f, r)
			selector := func(m ports.AutomaticTVSelectionMaterial) ports.AutomaticTVSelectionDecision {
				switch kind {
				case "target":
					m.Snapshot.UnitID = uuid.New()
				case "rules":
					m.Snapshot.Rules[0] = '['
				case "source-version":
					m.Snapshot.IndexerVersions[f.indexer.String()]++
				case "query":
					m.Candidates[0].QueryID = uuid.New()
				case "cipher":
					m.Candidates[0].Candidate.LinkEncrypted[0] ^= 1
				case "profile":
					m.Candidates[0].Candidate.QualityProfile[0] = '['
				case "candidate-id":
					m.Candidates[0].Candidate.ID = uuid.New()
				case "panic":
					panic("broken selector")
				}
				out := acquisition.SelectAutomaticTVEpisode(m)
				if kind == "false-negative" {
					out.CandidateID = nil
					out.Score = -1
					out.Reason = "no_qualified_candidate"
				}
				if kind == "fabricated" {
					v := uuid.New()
					out.CandidateID = &v
				}
				if kind == "blocked" || kind == "target" {
					// Rewriting a local target cannot admit an independently blocked row.
					out.CandidateID = &blocked.ID
				}
				return out
			}
			before := tvExecutionState(t, f)
			if _, e := tvExecutionStore(f, selector).FinalizeAutomaticTVRound(t.Context(), r.ID, lease); !errors.Is(e, ports.ErrAcquisitionRoundAborted) {
				t.Fatal(e)
			}
			if before != tvExecutionState(t, f) {
				t.Fatal("callback changed protected business state")
			}
			if _, e := tvExecutionStore(f, acquisition.SelectAutomaticTVEpisode).FinalizeAutomaticTVRound(t.Context(), r.ID, lease); e != nil {
				t.Fatal("original lease cannot recover", e)
			}
		})
	}
}

func TestAutomaticTVFinalizationCurrentTargetHoldsPG(t *testing.T) {
	cases := []struct{ name, sql string }{
		{"root-offline", `UPDATE library_root SET health='offline' WHERE id=$1`},
		{"root-generation", `UPDATE library_root SET generation=generation+1 WHERE id=$1`},
		{"root-health", `UPDATE library_root SET health_revision=health_revision+1 WHERE id=$1`},
		{"owner-revoked", `UPDATE app_user SET is_active=false WHERE id=$2`},
		{"paused", `UPDATE subscription SET state='paused' WHERE id=$3`},
		{"skipped", `UPDATE wanted_item SET is_skipped=true WHERE id=$4`},
		{"future", `UPDATE media_unit SET air_date=CURRENT_DATE+1 WHERE id=$5`},
		{"null-air", `UPDATE media_unit SET air_date=NULL WHERE id=$5`},
		{"absolute", `UPDATE media_unit SET absolute_number=3 WHERE id=$5`},
		{"manual-mapping", `INSERT INTO episode_mapping_revision(subscription_id,revision,media_id,media_version,intent_version,mode,input_stamp,source_hash,source_snapshot,mappings) SELECT s.id,2,s.media_id,m.metadata_version,s.intent_version+1,'manual',repeat('a',64),repeat('b',64),'{}',jsonb_build_array(jsonb_build_object('source_number',3,'unit_id',u.id,'unit_key',u.unit_key,'unit_version',u.metadata_version)) FROM subscription s JOIN media m ON m.id=s.media_id JOIN media_unit u ON u.id=$5 WHERE s.id=$3; UPDATE subscription SET episode_mapping_revision=2,intent_version=intent_version+1 WHERE id=$3; UPDATE wanted_item SET intent_version=(SELECT intent_version FROM subscription WHERE id=$3) WHERE id=$4`},
		{"retired", `UPDATE media_unit SET retired=true WHERE id=$5`},
		{"unit-version", `UPDATE media_unit SET metadata_version=metadata_version+1 WHERE id=$5`},
		{"media-version", `UPDATE media SET metadata_version=metadata_version+1 WHERE id=(SELECT media_id FROM subscription WHERE id=$3)`},
		{"policy", `INSERT INTO policy_revision(id,policy_id,revision,rules) SELECT gen_random_uuid(),quality_policy_id,quality_policy_revision+1,(SELECT rules FROM policy_revision WHERE policy_id=quality_policy_id AND revision=quality_policy_revision) FROM subscription WHERE id=$3; UPDATE policy SET current_revision=current_revision+1 WHERE id=(SELECT quality_policy_id FROM subscription WHERE id=$3)`},
		{"client", `UPDATE download_client SET url=url||'/changed' WHERE id=$6`},
		{"source-config", `UPDATE indexer SET config_version=config_version+1 WHERE id=$7`},
		{"source-offline", `UPDATE indexer SET health='offline' WHERE id=$7`},
		{"source-binding", `UPDATE subscription SET indexer_ids='[]' WHERE id=$3`},
		{"owner-transfer", `UPDATE subscription SET user_id=(SELECT id FROM app_user WHERE username='other-owner') WHERE id=$3`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newTVExecutionFixture(t)
			r := f.start(t)
			tvExecutionPage(t, f, r, tvExecutionCandidate(t, f, "good", "1080p", 1))
			lease := tvExecutionLease(t, f, r)
			values := []uuid.UUID{f.tv, uuid.MustParse(f.actor.ID), f.sub, f.wanted, f.unit, f.client, f.indexer}
			sql := tc.sql
			if tc.name == "owner-transfer" {
				if _, e := db.New(f.pool).CreateUser(t.Context(), db.CreateUserParams{ID: uuid.New(), Username: "other-owner", PasswordHash: "fixture", Role: ports.RoleMember, IsActive: true}); e != nil {
					t.Fatal(e)
				}
			}
			for i := len(values); i > 0; i-- {
				sql = strings.ReplaceAll(sql, fmt.Sprintf("$%d", i), fmt.Sprintf("'%s'::uuid", values[i-1]))
			}
			execIntent(t, f.pool, sql)
			before := tvExecutionState(t, f)
			if _, e := tvExecutionStore(f, acquisition.SelectAutomaticTVEpisode).FinalizeAutomaticTVRound(t.Context(), r.ID, lease); e == nil {
				t.Fatal("changed material authorized")
			}
			if before != tvExecutionState(t, f) {
				t.Fatal("hold wrote business state")
			}
			tvExecutionCounts(t, f, 0)
		})
	}
}

func TestAutomaticTVFinalizationNoCandidateStablePG(t *testing.T) {
	for _, kind := range []string{"empty", "other-tv", "changed-cipher", "changed-profile", "bad-hash"} {
		t.Run(kind, func(t *testing.T) {
			f := newTVExecutionFixture(t)
			r := f.start(t)
			c := tvExecutionCandidate(t, f, "proof", "1080p", 1)
			switch kind {
			case "other-tv":
				c.Title = "Trusted.Native.Series.2024.S02E03.{tmdb-9999}.1080p.WEB-DL"
			case "bad-hash":
				v := "invalid-infohash"
				c.InfoHash = &v
			}
			if kind == "empty" {
				tvExecutionPage(t, f, r)
			} else {
				tvExecutionPage(t, f, r, c)
			}
			if kind == "changed-cipher" {
				execIntent(t, f.pool, `UPDATE release_candidate SET link_encrypted='changed-cipher' WHERE id=$1`, c.ID)
			}
			if kind == "changed-profile" {
				execIntent(t, f.pool, `UPDATE release_candidate SET quality_profile='{}' WHERE id=$1`, c.ID)
			}
			lease := tvExecutionLease(t, f, r)
			repo := tvExecutionStore(f, acquisition.SelectAutomaticTVEpisode)
			got, e := repo.FinalizeAutomaticTVRound(t.Context(), r.ID, lease)
			if e != nil || got.State != "closed" || got.BestScore != -1 || got.Reason != "no_qualified_candidate" || got.OperationID != uuid.Nil {
				t.Fatalf("close %+v %v", got, e)
			}
			tvExecutionCounts(t, f, 0)
			before := tvExecutionState(t, f)
			again, e := repo.FinalizeAutomaticTVRound(t.Context(), r.ID, lease)
			if e != nil || !reflect.DeepEqual(got, again) || before != tvExecutionState(t, f) {
				t.Fatal("closed decision unstable", e)
			}
			if n := intentCount(t, f.pool, `SELECT count(*) FROM wanted_item WHERE id=$1 AND next_evaluate_at>clock_timestamp()+interval '4 minutes'`, f.wanted); n != 1 {
				t.Fatal("missing retry boundary")
			}
		})
	}
}

func TestAutomaticTVFinalizationConcurrentPG(t *testing.T) {
	f := newTVExecutionFixture(t)
	r := f.start(t)
	tvExecutionPage(t, f, r, tvExecutionCandidate(t, f, "good", "1080p", 1))
	lease := tvExecutionLease(t, f, r)
	repo := tvExecutionStore(f, acquisition.SelectAutomaticTVEpisode)
	var wg sync.WaitGroup
	results := make(chan *ports.RoundDecision, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() { d, e := repo.FinalizeAutomaticTVRound(t.Context(), r.ID, lease); results <- d; errs <- e })
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var first *ports.RoundDecision
	for d := range results {
		if first == nil {
			first = d
		} else if !reflect.DeepEqual(first, d) {
			t.Fatal("two decisions")
		}
	}
	tvExecutionCounts(t, f, 1)
	for _, table := range []string{"automatic_tv_decision", "audit_log WHERE action='automatic_tv.finalized'", "outbox_event WHERE event_type='automatic_tv.finalized'"} {
		if n := intentCount(t, f.pool, `SELECT count(*) FROM `+table); n != 1 {
			t.Fatal(table, n)
		}
	}
}

func TestAutomaticTVFinalizationTailFaultRollbackPG(t *testing.T) {
	for _, table := range []string{"audit_log", "outbox_event", "job"} {
		t.Run(table, func(t *testing.T) {
			f := newTVExecutionFixture(t)
			r := f.start(t)
			tvExecutionPage(t, f, r, tvExecutionCandidate(t, f, "good", "1080p", 1))
			lease := tvExecutionLease(t, f, r)
			condition := "NEW.action='automatic_tv.finalized'"
			operation := "INSERT"
			if table == "outbox_event" {
				condition = "NEW.event_type='automatic_tv.finalized'"
			}
			if table == "job" {
				condition = "NEW.kind='automatic_tv_finalize' AND NEW.state='succeeded'"
				operation = "UPDATE"
			}
			execIntent(t, f.pool, `CREATE FUNCTION tv_execution_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF `+condition+` THEN RAISE EXCEPTION 'tail fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER tv_execution_fault BEFORE `+operation+` ON `+table+` FOR EACH ROW EXECUTE FUNCTION tv_execution_fault()`)
			before := tvExecutionState(t, f)
			if _, e := tvExecutionStore(f, acquisition.SelectAutomaticTVEpisode).FinalizeAutomaticTVRound(t.Context(), r.ID, lease); !errors.Is(e, ports.ErrUnavailable) {
				t.Fatal(e)
			}
			if before != tvExecutionState(t, f) {
				t.Fatal("tail fault leaked graph")
			}
			execIntent(t, f.pool, `DROP TRIGGER tv_execution_fault ON `+table+`; DROP FUNCTION tv_execution_fault()`)
			if _, e := tvExecutionStore(f, acquisition.SelectAutomaticTVEpisode).FinalizeAutomaticTVRound(t.Context(), r.ID, lease); e != nil {
				t.Fatal("tail rollback not recoverable", e)
			}
		})
	}
}

func TestAutomaticTVFinalizationLeaseWaitAndTailPG(t *testing.T) {
	for _, kind := range []string{"lock-wait", "tail-expiry"} {
		t.Run(kind, func(t *testing.T) {
			f := newTVExecutionFixture(t)
			r := f.start(t)
			tvExecutionPage(t, f, r, tvExecutionCandidate(t, f, "good", "1080p", 1))
			lease := tvExecutionLease(t, f, r)
			if kind == "tail-expiry" {
				execIntent(t, f.pool, `CREATE FUNCTION tv_expire() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_type='automatic_tv.finalized' THEN UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE kind='automatic_tv_finalize'; END IF; RETURN NEW; END $$; CREATE TRIGGER tv_expire BEFORE INSERT ON outbox_event FOR EACH ROW EXECUTE FUNCTION tv_expire()`)
				before := tvExecutionState(t, f)
				if _, e := tvExecutionStore(f, acquisition.SelectAutomaticTVEpisode).FinalizeAutomaticTVRound(t.Context(), r.ID, lease); !errors.Is(e, ports.ErrAcquisitionRoundAborted) {
					t.Fatal(e)
				}
				if before != tvExecutionState(t, f) {
					t.Fatal("expired tail accepted writes")
				}
				return
			}
			tx, e := f.pool.Begin(t.Context())
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			if _, e = tx.Exec(t.Context(), `SELECT id FROM app_user WHERE id=$1 FOR UPDATE`, uuid.MustParse(f.actor.ID)); e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			done := make(chan error, 1)
			joined := false
			go func() {
				_, err := tvExecutionStore(f, acquisition.SelectAutomaticTVEpisode).FinalizeAutomaticTVRound(ctx, r.ID, lease)
				done <- err
			}()
			defer func() {
				_ = tx.Rollback(context.Background())
				cancel()
				if !joined {
					<-done
				}
			}()
			waitAutomaticTVBlocked(t, f.pool, tx.Conn().PgConn().PID())
			execIntent(t, f.pool, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, lease.ID)
			before := tvExecutionState(t, f)
			if e = tx.Commit(t.Context()); e != nil {
				t.Fatal(e)
			}
			e = <-done
			joined = true
			if !errors.Is(e, ports.ErrAcquisitionRoundAborted) || before != tvExecutionState(t, f) {
				t.Fatal("expired lock wait accepted", e)
			}
		})
	}
}
