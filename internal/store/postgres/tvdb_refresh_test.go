package postgres_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

type tvdbRefreshFixture struct {
	creationFixture
	store             *postgres.TVRefreshStore
	hint              ports.TVRefreshContext
	command           ports.TVRefreshCommand
	proof             ports.TVDBSeasonSupplement
	existing, omitted uuid.UUID
}

func newTVDBRefreshFixture(t *testing.T) tvdbRefreshFixture {
	t.Helper()
	f := newCreationFixture(t, 117)
	p := seedTVDBEvidence(t, f)
	q := db.New(f.pool)
	f.actor.Role = ports.RoleAdmin
	if _, err := f.pool.Exec(t.Context(), `UPDATE app_user SET role='admin' WHERE id=$1`, uuid.MustParse(f.actor.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE media_unit SET air_date='2008-03-09',absolute_number=107 WHERE id=$1`, p.UnitID); err != nil {
		t.Fatal(err)
	}
	omitted := uuid.New()
	if _, err := q.ApplyTVEpisodeMetadata(t.Context(), db.ApplyTVEpisodeMetadataParams{ID: omitted, MediaID: p.MediaID, UnitKey: "S01E09", SeasonNumber: pgtype.Int4{Int32: 1, Valid: true}, EpisodeNumber: pgtype.Int4{Int32: 9, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.BindTMDBEpisodeIdentity(t.Context(), db.BindTMDBEpisodeIdentityParams{UnitID: omitted, ExternalID: 709, TmdbSeriesID: 1396}); err != nil {
		t.Fatal(err)
	}
	sub, err := q.CreateSubscription(t.Context(), db.CreateSubscriptionParams{ID: uuid.New(), UserID: uuid.MustParse(f.actor.ID), MediaID: p.MediaID, RootID: f.tv, QualityPolicyID: "default", QualityPolicyRevision: 1, IndexerIds: []byte(`[]`), ScopeType: "all"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE subscription SET state='paused',intent_version=2 WHERE id=$1`, sub.ID); err != nil {
		t.Fatal(err)
	}
	for _, unit := range []uuid.UUID{p.UnitID, omitted} {
		if _, err := q.ApplyScopeWanted(t.Context(), db.ApplyScopeWantedParams{ID: uuid.New(), SubscriptionID: sub.ID, UnitID: unit, IntentVersion: 2}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE wanted_item SET user_skipped=true,is_skipped=true WHERE unit_id=$1`, p.UnitID); err != nil {
		t.Fatal(err)
	}
	store := postgres.NewTVRefreshStore(f.pool, idgen.New())
	command := ports.TVRefreshCommand{ExpectedVersion: 2, Seasons: []int{1}}
	hint, receipt, err := store.ReadTVRefreshContext(t.Context(), f.actor, sub.ID, command, "tvdb-refresh", "owned-fingerprint")
	if err != nil || receipt != nil {
		t.Fatal("refresh context", err)
	}
	added := ports.TVEpisodeMetadata{TMDBEpisodeID: 707, EpisodeNumber: 7}
	proof := ports.TVDBSeasonSupplement{TMDBSeriesID: 1396, TVDBSeriesID: 81189, CredentialVersion: 1, FallbackVersion: 1, Metadata: ports.TVSeasonMetadata{SeasonNumber: 1, Complete: false, Episodes: []ports.TVEpisodeMetadata{{TMDBEpisodeID: 701, EpisodeNumber: 107}, added}}, Added: []ports.TVDBEpisodeResolution{{TMDBSeriesID: 1396, TVDBEpisodeID: 9007, SeasonNumber: 1, Episode: added}}, UnmatchedTVDBIDs: []int64{9008}}
	return tvdbRefreshFixture{creationFixture: f, store: store, hint: hint, command: command, proof: proof, existing: p.UnitID, omitted: omitted}
}
func (f tvdbRefreshFixture) apply(t *testing.T) (*ports.TVRefreshReceipt, error) {
	t.Helper()
	return f.store.ApplyTVRefreshWithSupplements(t.Context(), f.actor, f.hint, f.command, []ports.TVSeasonMetadata{f.proof.Metadata}, []ports.TVDBSeasonSupplement{f.proof}, "tvdb-refresh", "owned-fingerprint")
}
func tvdbRefreshSnapshot(t *testing.T, f tvdbRefreshFixture) string {
	t.Helper()
	var result string
	const query = `SELECT jsonb_build_array(
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM media x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM media_unit x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY unit_id,namespace) FROM media_unit_identity x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM wanted_item x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY media_id) FROM tvdb_series_identity x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY unit_id) FROM tvdb_episode_evidence x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM audit_log x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM outbox_event x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM idempotency_record x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM job x),
 (SELECT count(*) FROM tv_season_metadata_snapshot))::text`
	if err := f.pool.QueryRow(t.Context(), query).Scan(&result); err != nil {
		t.Fatal(err)
	}
	return result
}
func TestTVDBSupplementRefreshAtomicCommitAndOfflineReceiptPG(t *testing.T) {
	f := newTVDBRefreshFixture(t)
	receipt, err := f.apply(t)
	if err != nil || receipt == nil || receipt.ObservedEpisodes != 2 || receipt.MediaVersion != f.hint.MediaVersion+1 {
		t.Fatal("supplement commit", receipt, err)
	}
	var units, wanted, proofs, audits, events, keys, jobs, snapshots int
	if err := f.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM media_unit),(SELECT count(*) FROM wanted_item),(SELECT count(*) FROM tvdb_episode_evidence),(SELECT count(*) FROM audit_log),(SELECT count(*) FROM outbox_event),(SELECT count(*) FROM idempotency_record),(SELECT count(*) FROM job),(SELECT count(*) FROM tv_season_metadata_snapshot)`).Scan(&units, &wanted, &proofs, &audits, &events, &keys, &jobs, &snapshots); err != nil {
		t.Fatal(err)
	}
	if units != 3 || wanted != 3 || proofs != 1 || audits != 1 || events != 1 || keys != 1 || jobs != 0 || snapshots != 0 {
		t.Fatal("wrong atomic ledger", units, wanted, proofs, audits, events, keys, jobs, snapshots)
	}
	var date, state string
	var absolute int
	var omittedRetired, skipped, unknown bool
	if err := f.pool.QueryRow(t.Context(), `SELECT (SELECT air_date::text FROM media_unit WHERE id=$1),(SELECT absolute_number FROM media_unit WHERE id=$1),(SELECT retired FROM media_unit WHERE id=$2),(SELECT is_skipped FROM wanted_item WHERE unit_id=$1),(SELECT state FROM subscription WHERE id=$3),(SELECT air_date IS NULL AND absolute_number IS NULL FROM media_unit WHERE media_id=$4 AND unit_key='S01E07')`, f.existing, f.omitted, f.hint.SubscriptionID, f.hint.MediaID).Scan(&date, &absolute, &omittedRetired, &skipped, &state, &unknown); err != nil {
		t.Fatal(err)
	}
	if date != "2008-03-09" || absolute != 107 || omittedRetired || !skipped || state != "paused" || !unknown {
		t.Fatal("existing facts or paused intent changed", date, absolute, omittedRetired, skipped, state, unknown)
	}
	if _, err := f.pool.Exec(t.Context(), `DELETE FROM setting WHERE key IN ('tvdb.credentials','tvdb.fallback')`); err != nil {
		t.Fatal(err)
	}
	before := tvdbRefreshSnapshot(t, f)
	replay, err := f.store.ApplyTVRefreshWithSupplements(t.Context(), f.actor, f.hint, f.command, nil, nil, "tvdb-refresh", "owned-fingerprint")
	if err != nil || replay == nil || *replay != *receipt || tvdbRefreshSnapshot(t, f) != before {
		t.Fatal("offline original receipt mutated work", err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE app_user SET role='member' WHERE id=$1`, uuid.MustParse(f.actor.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ApplyTVRefreshWithSupplements(t.Context(), f.actor, f.hint, f.command, nil, nil, "tvdb-refresh", "owned-fingerprint"); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("receipt ignored live revocation", err)
	}
	if tvdbRefreshSnapshot(t, f) != before {
		t.Fatal("revoked replay changed business data")
	}
}
func TestTVDBSupplementRefreshRejectsLateAuthorizationPG(t *testing.T) {
	for _, scenario := range []string{"disable", "credentials_version", "permission", "media_version", "source_identity", "claimed_complete", "mismatched_proof"} {
		t.Run(scenario, func(t *testing.T) {
			f := newTVDBRefreshFixture(t)
			q := db.New(f.pool)
			var err error
			switch scenario {
			case "disable":
				_, err = q.SetSetting(t.Context(), db.SetSettingParams{Key: "tvdb.fallback", Value: []byte(`{"enabled":false}`)})
			case "credentials_version":
				_, err = q.SetSetting(t.Context(), db.SetSettingParams{Key: "tvdb.credentials", Value: []byte(`{"ciphertext":"different-opaque-setting"}`)})
			case "permission":
				_, err = f.pool.Exec(t.Context(), `UPDATE app_user SET role='member' WHERE id=$1`, uuid.MustParse(f.actor.ID))
			case "media_version":
				_, err = q.AdvanceTVMediaMetadataVersion(t.Context(), f.hint.MediaID)
			case "source_identity":
				_, err = q.BindTVDBSeriesIdentity(t.Context(), db.BindTVDBSeriesIdentityParams{MediaID: f.hint.MediaID, TmdbSeriesID: 1396, TvdbSeriesID: 99999})
			case "claimed_complete":
				f.proof.Metadata.Complete = true
			case "mismatched_proof":
				f.proof.Added[0].Episode.EpisodeNumber = 8
			}
			if err != nil {
				t.Fatal(err)
			}
			before := tvdbRefreshSnapshot(t, f)
			receipt, err := f.apply(t)
			if receipt != nil || err == nil {
				t.Fatal("late or unproven supplement accepted", receipt, err)
			}
			if scenario == "permission" && !errors.Is(err, ports.ErrForbidden) {
				t.Fatal("revocation misclassified", err)
			}
			if tvdbRefreshSnapshot(t, f) != before {
				t.Fatal("rejected refresh changed business graph")
			}
		})
	}
}
func TestTVDBSupplementRefreshTailRollbackPG(t *testing.T) {
	for _, table := range []string{"audit_log", "outbox_event", "idempotency_record"} {
		t.Run(table, func(t *testing.T) {
			f := newTVDBRefreshFixture(t)
			statement := fmt.Sprintf(`CREATE FUNCTION fail_tvdb_refresh() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'owned tail failure'; END$$; CREATE TRIGGER fail_tvdb_refresh BEFORE INSERT ON %s FOR EACH ROW EXECUTE FUNCTION fail_tvdb_refresh()`, table)
			if _, err := f.pool.Exec(t.Context(), statement); err != nil {
				t.Fatal(err)
			}
			before := tvdbRefreshSnapshot(t, f)
			if receipt, err := f.apply(t); receipt != nil || !errors.Is(err, ports.ErrUnavailable) {
				t.Fatal("tail failure escaped contract", receipt, err)
			}
			if tvdbRefreshSnapshot(t, f) != before {
				t.Fatal("tail failure left units, demand, evidence or receipts")
			}
			if _, err := f.pool.Exec(t.Context(), fmt.Sprintf(`DROP TRIGGER fail_tvdb_refresh ON %s`, table)); err != nil {
				t.Fatal(err)
			}
			if receipt, err := f.apply(t); receipt == nil || err != nil {
				t.Fatal("same command could not retry after rollback", err)
			}
		})
	}
}
func TestTVDBSupplementRefreshLaterPrimaryOmissionPG(t *testing.T) {
	f := newTVDBRefreshFixture(t)
	if _, err := f.apply(t); err != nil {
		t.Fatal(err)
	}
	hint, _, err := f.store.ReadTVRefreshContext(t.Context(), f.actor, f.hint.SubscriptionID, f.command, "later-primary", "later-fingerprint")
	if err != nil {
		t.Fatal(err)
	}
	full := ports.TVSeasonMetadata{SeasonNumber: 1, Complete: true, Episodes: []ports.TVEpisodeMetadata{{TMDBEpisodeID: 701, EpisodeNumber: 107}}}
	if _, err := f.store.ApplyTVRefresh(t.Context(), f.actor, hint, f.command, []ports.TVSeasonMetadata{full}, "later-primary", "later-fingerprint"); err != nil {
		t.Fatal(err)
	}
	var protected, ordinary bool
	var snapshots int
	if err := f.pool.QueryRow(t.Context(), `SELECT (SELECT retired FROM media_unit WHERE media_id=$1 AND unit_key='S01E07'),(SELECT retired FROM media_unit WHERE id=$2),(SELECT count(*) FROM tv_season_metadata_snapshot)`, f.hint.MediaID, f.omitted).Scan(&protected, &ordinary, &snapshots); err != nil {
		t.Fatal(err)
	}
	if protected || !ordinary || snapshots != 0 {
		t.Fatal("partial source authorized retirement or complete season", protected, ordinary, snapshots)
	}
	var body string
	if err := f.pool.QueryRow(t.Context(), `SELECT response_body::text FROM idempotency_record WHERE idempotency_key='tvdb-refresh'`).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "TVDB") || strings.Contains(body, "credential") {
		t.Fatal("legacy public receipt shape changed")
	}
}
