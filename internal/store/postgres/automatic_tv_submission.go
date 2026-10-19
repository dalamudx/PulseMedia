package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type AutomaticTVSubmissionStore struct {
	pool    *pgxpool.Pool
	ids     ports.IDGen
	decoder ports.CredentialDecoder
}

func NewAutomaticTVSubmissionStore(pool *pgxpool.Pool, ids ports.IDGen, decoder ports.CredentialDecoder) *AutomaticTVSubmissionStore {
	return &AutomaticTVSubmissionStore{pool: pool, ids: ids, decoder: decoder}
}

var _ ports.AutomaticTVSubmissionRepository = (*AutomaticTVSubmissionStore)(nil)

type automaticTVPreparedProof struct {
	Kind       string                              `json:"kind"`
	Authority  ports.AutomaticTVExecutionAuthority `json:"authority"`
	SavePath   string                              `json:"save_path"`
	InfoHash   string                              `json:"info_hash"`
	SHA256     [32]byte                            `json:"sha256"`
	TotalBytes int64                               `json:"total_bytes"`
	Files      []media.TVTorrentFile               `json:"files"`
	Video      media.TVTorrentUnitFile             `json:"video"`
}

func automaticTVSubmissionError(ctx context.Context, e error, stale bool) error {
	if e == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(e, ports.ErrAutomaticTVNeedsObservation) {
		return e
	}
	if errors.Is(e, pgx.ErrNoRows) || errors.Is(e, ports.ErrAcquisitionRoundAborted) || errors.Is(e, ports.ErrAutomaticTVSubmissionReview) || errors.Is(e, ports.ErrAutomaticTVResultStale) {
		if stale {
			return ports.ErrAutomaticTVResultStale
		}
		return ports.ErrAutomaticTVSubmissionReview
	}
	return ports.ErrUnavailable
}

func automaticTVSendRequestValid(r ports.AutomaticTVSendRequest) bool {
	return r.OperationID != uuid.Nil && r.Lease.ID != uuid.Nil && r.Lease.Owner != "" && r.Lease.Token > 0
}

func automaticTVSubmissionAuthority(ctx context.Context, q *db.Queries, id uuid.UUID) (ports.AutomaticTVExecutionAuthority, error) {
	var a ports.AutomaticTVExecutionAuthority
	r, e := q.AutomaticTVSubmissionAuthority(ctx, id)
	if e != nil {
		return a, e
	}
	if json.Unmarshal(r.Snapshot, &a) != nil {
		return a, ports.ErrAutomaticTVSubmissionReview
	}
	// Canonicalization unmarshals into RawMessage; keep its reused buffer private.
	canonical := a
	canonical.Target.Rules = bytes.Clone(a.Target.Rules)
	raw, e := automaticTVAuthorityBytes(canonical)
	if e != nil || completeTVJSONDigest(raw) != completeTVJSONDigest(r.Snapshot) || a.SchemaVersion != "automatic-tv-execution-v1" || a.Kind != "automatic_standard_single_episode_v1" || a.OperationID != r.OperationID || a.RoundID != r.RoundID || a.Target.OwnerID != r.OwnerID || a.Target.SubscriptionID != r.SubscriptionID || a.Target.UnitID != r.UnitID || a.Target.RootID != r.RootID || a.CandidateID != r.CandidateID || a.IndexerID != r.IndexerID || a.Target.ClientID != r.ClientID || a.QueryID != r.QueryID || a.DecisionStamp != r.DecisionStamp || a.SendValidUntil.Sub(a.IssuedAt) != 5*time.Minute {
		return a, ports.ErrAutomaticTVSubmissionReview
	}
	return a, nil
}

func automaticTVSubmissionJob(ctx context.Context, q *db.Queries, r ports.AutomaticTVSendRequest, a ports.AutomaticTVExecutionAuthority) error {
	payload, _ := json.Marshal(automaticTVSubmissionPayload{OperationID: a.OperationID, RoundID: a.RoundID, Authority: a.OperationID, CandidateID: a.CandidateID, RootID: a.Target.RootID, ClientID: a.Target.ClientID})
	ok, e := q.AutomaticTVSubmissionJobValid(ctx, db.AutomaticTVSubmissionJobValidParams{JobID: r.Lease.ID, Owner: r.Lease.Owner, Token: r.Lease.Token, OperationID: r.OperationID, Payload: payload})
	if e != nil {
		return e
	}
	if !ok {
		return ports.ErrAutomaticTVSubmissionReview
	}
	return nil
}

// An immutable original query is the source of these bytes, never another query's latest candidate.
func automaticTVSubmissionOriginal(ctx context.Context, tx pgx.Tx, a ports.AutomaticTVExecutionAuthority) error {
	var valid bool
	e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM automatic_tv_round r
 JOIN automatic_tv_decision d ON d.round_id=r.id
 JOIN automatic_tv_query q ON q.round_id=r.id
 JOIN indexer_query iq ON iq.id=q.query_id
 JOIN automatic_tv_source s ON s.round_id=r.id AND s.indexer_id=q.indexer_id
 JOIN automatic_tv_query_candidate f ON f.query_id=q.query_id
 JOIN indexer_query_candidate qc ON qc.query_id=q.query_id AND qc.candidate_id=f.candidate_id
 WHERE r.id=$1 AND r.state='closed' AND r.snapshot_hash=$2 AND r.snapshot=$3::jsonb
 AND d.state='reserved' AND d.operation_id=$4 AND d.candidate_id=$5
 AND q.query_id=$6 AND q.indexer_id=$7 AND q.parameters_hash=$8
 AND iq.query_type='tv_episode' AND iq.state='succeeded' AND iq.config_version=$9
 AND iq.keyword='tv-episode-query-v1:'||q.parameters_hash
 AND s.config_version=$9 AND f.candidate_id=$5 AND f.source_stamp=$10)`, a.RoundID, a.RoundSnapshotHash, mustAutomaticTVJSON(a.Target), a.OperationID, a.CandidateID, a.QueryID, a.IndexerID, a.QueryParametersHash, a.SourceConfigVersion, a.SourceStamp).Scan(&valid)
	if e != nil {
		return e
	}
	if !valid {
		return ports.ErrAutomaticTVSubmissionReview
	}
	return nil
}
func mustAutomaticTVJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func automaticTVSubmissionNoPrior(ctx context.Context, tx pgx.Tx, op uuid.UUID, allowAttempt uuid.UUID) error {
	var exists bool
	e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM download_task WHERE operation_id=$1)
 OR EXISTS(SELECT 1 FROM automatic_tv_submission_preparation WHERE operation_id=$1)
 OR EXISTS(SELECT 1 FROM automatic_tv_physical_attempt WHERE operation_id=$1 AND finished_at IS NULL AND id<>$2)`, op, allowAttempt).Scan(&exists)
	if e != nil {
		return e
	}
	if exists {
		return ports.ErrAutomaticTVNeedsObservation
	}
	return nil
}

func automaticTVSubmissionPhysical(ctx context.Context, tx pgx.Tx, q *db.Queries, r ports.AutomaticTVSendRequest, id uuid.UUID, held bool) error {
	if id == uuid.Nil {
		return ports.ErrAutomaticTVSubmissionReview
	}
	x, e := q.AutomaticTVSubmissionAttempt(ctx, id)
	if e != nil {
		return e
	}
	digest := sha256.Sum256([]byte("pulsemedia.automatic-tv.physical.v1:" + r.OperationID.String()))
	key := int64(binary.BigEndian.Uint64(digest[:8]))
	if x.OperationID != r.OperationID || x.JobID != r.Lease.ID || x.LeaseOwner != r.Lease.Owner || x.LeaseToken != r.Lease.Token || x.FinishedAt.Valid || x.AdvisoryKey != key {
		return ports.ErrAutomaticTVSubmissionReview
	}
	if held {
		var ok bool
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND pid=$1 AND granted AND mode='ExclusiveLock' AND objsubid=1 AND classid=($2::bigint>>32 & 4294967295)::oid AND objid=($2::bigint & 4294967295)::oid AND database=(SELECT oid FROM pg_database WHERE datname=current_database()))`, x.BackendPid, key).Scan(&ok)
		if e != nil {
			return e
		}
		if !ok {
			return ports.ErrAutomaticTVSubmissionReview
		}
	}
	return nil
}

func automaticTVSubmissionProtections(ctx context.Context, tx pgx.Tx, q *db.Queries, a ports.AutomaticTVExecutionAuthority, task uuid.UUID) error {
	fail := ports.ErrAutomaticTVSubmissionReview
	t := a.Target
	lanes, e := q.ListCompleteTVPreparationLanes(ctx, a.OperationID)
	if e != nil {
		return e
	}
	reservations, e := q.ListCompleteTVPreparationReservations(ctx, a.OperationID)
	if e != nil {
		return e
	}
	demands, e := q.AutomaticTVSubmissionRequirements(ctx, a.OperationID)
	if e != nil {
		return e
	}
	if len(lanes) != 1 || len(reservations) != 1 || len(demands) != 1 {
		return fail
	}
	l, r, d := lanes[0], reservations[0], demands[0]
	fence := automaticTVDemandFence{DownloadDemandFence: ports.DownloadDemandFence{PolicyID: t.PolicyID, PolicyRevision: t.PolicyRevision, SubscriptionID: &t.SubscriptionID, IntentVersion: t.IntentVersion, RootGeneration: t.RootGeneration, UnitMetadataVersion: t.UnitVersion, MediaMetadataVersion: t.MediaVersion, CandidateID: a.CandidateID, RootID: t.RootID, UnitID: t.UnitID, MediaID: t.MediaID, ActorID: a.RoundID.String(), ActorType: ports.ActorTypeSystem, SourceStamp: a.SourceStamp}, Goal: "acquire", Kind: "automatic_tv_episode_v1", Authority: a.OperationID, ProtectedDigest: a.DecisionStamp}
	state := "reserved"
	if task != uuid.Nil {
		state = "dispatched"
	}
	if l.UnitID != t.UnitID || l.AcquisitionLane != "default" || l.LegacyConflict || l.SourceRootID != t.RootID || l.SourceGeneration != t.RootGeneration || l.SourceStamp != a.SourceStamp || l.CandidateID != a.CandidateID || l.TaskID.Valid != (task != uuid.Nil) || (l.TaskID.Valid && uuid.UUID(l.TaskID.Bytes) != task) || r.UnitID != t.UnitID || r.RootID != t.RootID || r.CandidateID != a.CandidateID || r.Purpose != "acquire" || r.State != state || d.UnitID != t.UnitID || d.RootID != t.RootID || d.ActorType != "system" || d.ActorID != a.RoundID.String() || d.State != "pending" || completeTVJSONDigest(d.SubmissionFence) != completeTVJSONDigest(mustAutomaticTVJSON(fence)) {
		return fail
	}
	if task == uuid.Nil {
		var clear bool
		e = tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM acquisition_reservation WHERE unit_id=$1 AND released_at IS NULL AND operation_id<>$2)
 AND NOT EXISTS(SELECT 1 FROM unit_reservation WHERE unit_id=$1 AND state IN ('reserved','dispatched') AND operation_id<>$2)
 AND NOT EXISTS(SELECT 1 FROM download_requirement WHERE unit_id=$1 AND state='pending' AND operation_id<>$2)
 AND NOT EXISTS(SELECT 1 FROM library_file_unit fu JOIN library_file f ON f.id=fu.file_id WHERE fu.unit_id=$1 AND fu.retired_at IS NULL AND f.retired_at IS NULL)
 AND NOT EXISTS(SELECT 1 FROM library_reuse_unit WHERE unit_id=$1 AND active)
 AND $3::timestamptz>clock_timestamp()`, t.UnitID, a.OperationID, r.ExpiresAt).Scan(&clear)
		if e != nil {
			return e
		}
		if !clear {
			return fail
		}
	}
	return nil
}

// Locks follow owner -> original job -> policy -> subscription -> root -> unit -> wanted -> operation -> media -> indexer -> candidate -> client -> protections.
func (s *AutomaticTVSubmissionStore) automaticTVSendFacts(ctx context.Context, tx pgx.Tx, q *db.Queries, r ports.AutomaticTVSendRequest, a ports.AutomaticTVExecutionAuthority, attempt uuid.UUID) (*ports.AutomaticTVSendWork, error) {
	fail := ports.ErrAutomaticTVSubmissionReview
	t := a.Target
	if _, e := automaticTVLockOwner(ctx, q, t.OwnerID); e != nil {
		return nil, e
	}
	if _, e := q.LockJobForWrite(ctx, r.Lease.ID); e != nil {
		return nil, e
	}
	if e := automaticTVSubmissionJob(ctx, q, r, a); e != nil {
		return nil, e
	}
	policy, e := q.LockSearchPolicy(ctx, t.PolicyID)
	if e != nil {
		return nil, e
	}
	sub, e := q.LockSearchSubscription(ctx, t.SubscriptionID)
	if e != nil {
		return nil, e
	}
	if sub.UserID != t.OwnerID || sub.State != "active" || sub.IntentVersion != t.IntentVersion || sub.MediaID != t.MediaID || sub.RootID != t.RootID || sub.QualityPolicyID != t.PolicyID || sub.QualityPolicyRevision != t.PolicyRevision || policy.CurrentRevision != t.PolicyRevision || sub.UpgradeMode != "disabled" || !sub.DownloaderID.Valid || uuid.UUID(sub.DownloaderID.Bytes) != t.ClientID || sub.EpisodeMappingRevision.Int64 != t.MappingRevision || (sub.EpisodeMappingRevision.Valid != (t.MappingRevision > 0)) {
		return nil, fail
	}
	rev, e := q.GetPolicyRevision(ctx, db.GetPolicyRevisionParams{PolicyID: t.PolicyID, Revision: t.PolicyRevision})
	if e != nil {
		return nil, e
	}
	if completeTVJSONDigest(rev.Rules) != completeTVJSONDigest(t.Rules) {
		return nil, fail
	}
	root, e := q.LockAcquisitionRoot(ctx, t.RootID)
	if e != nil {
		return nil, e
	}
	if root.MediaType != "tv" || root.Health != "online" || !root.BaselineCompleted || root.Generation != t.RootGeneration || root.HealthRevision != t.RootHealthRevision || automaticTVDigest([]byte(root.Path)) != a.RootPathDigest {
		return nil, fail
	}
	u, e := q.LockAcquisitionUnit(ctx, t.UnitID)
	if e != nil {
		return nil, e
	}
	w, e := q.LockSearchWanted(ctx, t.WantedID)
	if e != nil {
		return nil, e
	}
	if u.MediaID != t.MediaID || u.MetadataVersion != t.UnitVersion || u.UnitKey != t.UnitKey || u.Retired || !u.SeasonNumber.Valid || u.SeasonNumber.Int32 != t.Season || !u.EpisodeNumber.Valid || u.EpisodeNumber.Int32 != t.Episode || u.AbsoluteNumber.Valid || !u.AirDate.Valid || !u.AirDate.Time.Equal(t.AirDate) || w.SubscriptionID != sub.ID || w.UnitID != u.ID || w.IsSkipped || w.UserSkipped || w.ScopeExcluded || w.IntentVersion != t.IntentVersion {
		return nil, fail
	}
	aired, e := q.AutomaticTVAired(ctx, u.ID)
	if e != nil {
		return nil, e
	}
	if !aired.Valid || !aired.Bool {
		return nil, fail
	}
	if e = q.LockDownloadAcquisition(ctx, a.OperationID); e != nil {
		return nil, e
	}
	if e = automaticTVSubmissionNoPrior(ctx, tx, a.OperationID, attempt); e != nil {
		return nil, e
	}
	m, e := q.LockSearchMedia(ctx, t.MediaID)
	if e != nil {
		return nil, e
	}
	if m.MediaType != "tv" || m.MetadataVersion != t.MediaVersion || m.Title != t.Title || m.OriginalTitle.String != t.OriginalTitle || !m.Year.Valid || m.Year.Int32 != t.Year || t.MappingMode != "standard" {
		return nil, fail
	}
	identities, e := q.ListSearchMediaIdentities(ctx, m.ID)
	if e != nil {
		return nil, e
	}
	match := false
	for _, i := range identities {
		if i.Namespace == "tmdb.tv" && i.ExternalID == strconv.FormatInt(t.TMDBID, 10) {
			match = true
		}
	}
	episode, e := q.GetTMDBEpisodeIdentityForUnit(ctx, u.ID)
	if e != nil {
		return nil, e
	}
	if !match || episode.ExternalID != t.EpisodeID || episode.TmdbSeriesID != t.TMDBID {
		return nil, fail
	}
	if t.MappingRevision > 0 {
		mapping, e := q.GetCurrentEpisodeMapping(ctx, sub.ID)
		if e != nil {
			return nil, e
		}
		if mapping.Revision != t.MappingRevision || mapping.Mode != "standard" || mapping.MediaID != m.ID || mapping.MediaVersion != m.MetadataVersion {
			return nil, fail
		}
	}
	var bound []uuid.UUID
	if json.Unmarshal(sub.IndexerIds, &bound) != nil || !slices.Contains(bound, a.IndexerID) || t.IndexerVersions[a.IndexerID.String()] != a.SourceConfigVersion {
		return nil, fail
	}
	idx, e := q.LockSearchIndexer(ctx, a.IndexerID)
	if e != nil {
		return nil, e
	}
	current, e := q.AutomaticTVSourceCurrent(ctx, db.AutomaticTVSourceCurrentParams{ID: idx.ID, ConfigVersion: a.SourceConfigVersion})
	if e != nil {
		return nil, e
	}
	if !current {
		return nil, fail
	}
	candidate, e := q.LockSubmissionCandidate(ctx, a.CandidateID)
	if e != nil {
		return nil, e
	}
	var same bool
	e = tx.QueryRow(ctx, `SELECT f.title=c.title AND f.guid=c.guid AND f.info_hash IS NOT DISTINCT FROM c.info_hash AND f.link_encrypted=c.link_encrypted AND f.quality_profile=c.quality_profile AND f.size_bytes=c.size_bytes AND f.source_config_version=c.source_config_version FROM automatic_tv_query_candidate f JOIN release_candidate c ON c.id=f.candidate_id WHERE f.query_id=$1 AND f.candidate_id=$2`, a.QueryID, a.CandidateID).Scan(&same)
	if e != nil {
		return nil, e
	}
	row := ports.AutomaticTVRoundCandidate{Candidate: automaticTVSearchCandidate(candidate), QueryID: a.QueryID, QueryParametersHash: a.QueryParametersHash, QuerySourceStamp: a.SourceStamp}
	if _, ok := automaticTVEligible(t, row); !same || !ok || candidate.IndexerID != a.IndexerID {
		return nil, fail
	}
	if e = automaticTVSubmissionOriginal(ctx, tx, a); e != nil {
		return nil, e
	}
	if _, e = q.GetSearchDownloaderSnapshot(ctx, t.ClientID); e != nil {
		return nil, e
	}
	client, e := q.GetDownloadClientByID(ctx, t.ClientID)
	if e != nil {
		return nil, e
	}
	var pathmap map[string]string
	if episodeClientStamp(client) != t.ClientStamp || json.Unmarshal(client.RemotePathMap, &pathmap) != nil || (client.ClientType != "qbittorrent" && client.ClientType != "transmission") {
		return nil, fail
	}
	save := naming.TranslateRemotePath(pathmap, root.Path)
	if save == "" {
		return nil, fail
	}
	if e = automaticTVSubmissionProtections(ctx, tx, q, a, uuid.Nil); e != nil {
		return nil, e
	}
	if s.decoder == nil {
		return nil, ports.ErrUnavailable
	}
	resolved, modes, e := ResolveExecutableIndexerConfiguration(ctx, q, s.decoder, idx)
	if e != nil || modes.Protocol != ports.IndexerProtocolTorznab {
		return nil, ports.ErrUnavailable
	}
	// Check envelopes here, before returning any material to network code.
	for _, secret := range [][]byte{client.PasswordEncrypted, idx.ApiKeyEncrypted, candidate.LinkEncrypted} {
		if _, e = s.decoder.DecryptSecret(bytes.Clone(secret)); e != nil {
			return nil, ports.ErrUnavailable
		}
	}
	link, e := s.decoder.DecryptSecret(bytes.Clone(candidate.LinkEncrypted))
	if e != nil || !automaticTVSubmissionURL(string(link)) || !automaticTVSubmissionURL(resolved.Url) || !automaticTVSubmissionURL(client.Url) {
		return nil, ports.ErrUnavailable
	}
	if e = automaticTVSubmissionTail(ctx, tx, q, r, a, true); e != nil {
		return nil, e
	}
	return &ports.AutomaticTVSendWork{Request: r, Authority: a, RootPath: root.Path, SavePath: save, ClientType: client.ClientType, ClientURL: client.Url, ClientUsername: client.Username, ClientPasswordEncrypted: bytes.Clone(client.PasswordEncrypted), SourceEndpointURL: resolved.Url, SourceAPIKeyEncrypted: bytes.Clone(idx.ApiKeyEncrypted), DownloadURLEncrypted: bytes.Clone(candidate.LinkEncrypted)}, nil
}

func automaticTVSubmissionURL(raw string) bool {
	u, e := url.Parse(raw)
	return e == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != "" && u.Opaque == "" && u.Fragment == "" && !strings.ContainsAny(raw, "\r\n\x00")
}

func automaticTVSubmissionTail(ctx context.Context, tx pgx.Tx, q *db.Queries, r ports.AutomaticTVSendRequest, a ports.AutomaticTVExecutionAuthority, first bool) error {
	if e := automaticTVSubmissionJob(ctx, q, r, a); e != nil {
		return e
	}
	if first {
		var valid bool
		e := tx.QueryRow(ctx, `SELECT clock_timestamp()<$1::timestamptz AND state='running' AND lease_owner=$3 AND lease_token=$4 AND lease_until>clock_timestamp() FROM job WHERE id=$2`, a.SendValidUntil, r.Lease.ID, r.Lease.Owner, r.Lease.Token).Scan(&valid)
		if e != nil {
			return e
		}
		if !valid {
			return ports.ErrAutomaticTVSubmissionReview
		}
	}
	return nil
}

func (s *AutomaticTVSubmissionStore) LoadAutomaticTVSendWork(ctx context.Context, r ports.AutomaticTVSendRequest) (out *ports.AutomaticTVSendWork, err error) {
	defer func() {
		err = automaticTVSubmissionError(ctx, err, false)
		if err != nil {
			out = nil
		}
	}()
	if s == nil || s.pool == nil || s.ids == nil {
		return nil, ports.ErrUnavailable
	}
	if !automaticTVSendRequestValid(r) {
		return nil, ports.ErrAutomaticTVSubmissionReview
	}
	a, e := automaticTVSubmissionAuthority(ctx, db.New(s.pool), r.OperationID)
	if e != nil {
		return nil, e
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer managementRollback(ctx, tx)
	if e = automaticTVSubmissionNoPrior(ctx, tx, r.OperationID, uuid.Nil); e != nil {
		return nil, e
	}
	out, e = s.automaticTVSendFacts(ctx, tx, db.New(tx), r, a, uuid.Nil)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return out, nil
}

func automaticTVMakePrepared(a ports.AutomaticTVExecutionAuthority, save string, p *ports.AutomaticTVMetainfoProof) (automaticTVPreparedProof, error) {
	var proof automaticTVPreparedProof
	if !p.ValidateBinding() {
		return proof, ports.ErrAutomaticTVSubmissionReview
	}
	t := a.Target
	video, e := media.ValidateAutomaticTVSingleEpisodeFiles(p.Files(), media.AutomaticTVTorrentTarget{Unit: media.NumberingUnit{ID: t.UnitID, EpisodeID: t.EpisodeID, Season: int(t.Season), Episode: int(t.Episode), Key: t.UnitKey, Version: t.UnitVersion}, Title: t.Title, OriginalTitle: t.OriginalTitle, Year: t.Year, TMDBID: t.TMDBID})
	if e != nil {
		return proof, ports.ErrAutomaticTVSubmissionReview
	}
	return automaticTVPreparedProof{Kind: "automatic_tv_prepared_v1", Authority: a, SavePath: save, InfoHash: p.InfoHash(), SHA256: p.SHA256(), TotalBytes: p.TotalBytes(), Files: p.Files(), Video: *video}, nil
}

func (s *AutomaticTVSubmissionStore) PrepareAutomaticTVSubmission(ctx context.Context, c ports.PrepareAutomaticTVSubmissionCommand) (out *ports.AutomaticTVSubmissionReceipt, err error) {
	defer func() {
		err = automaticTVSubmissionError(ctx, err, false)
		if err != nil {
			out = nil
		}
	}()
	if s == nil || s.pool == nil || s.ids == nil {
		return nil, ports.ErrUnavailable
	}
	if !automaticTVSendRequestValid(c.Request) || c.TaskID == uuid.Nil || c.PhysicalAttemptID == uuid.Nil || !c.Metainfo.ValidateBinding() {
		return nil, ports.ErrAutomaticTVSubmissionReview
	}
	a, e := automaticTVSubmissionAuthority(ctx, db.New(s.pool), c.Request.OperationID)
	if e != nil {
		return nil, e
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	if e = automaticTVSubmissionNoPrior(ctx, tx, c.Request.OperationID, c.PhysicalAttemptID); e != nil {
		return nil, e
	}
	work, e := s.automaticTVSendFacts(ctx, tx, q, c.Request, a, c.PhysicalAttemptID)
	if e != nil {
		return nil, e
	}
	if e = automaticTVSubmissionPhysical(ctx, tx, q, c.Request, c.PhysicalAttemptID, true); e != nil {
		return nil, e
	}
	candidate, e := q.GetCandidateByID(ctx, a.CandidateID)
	if e != nil {
		return nil, e
	}
	if candidate.SizeBytes != c.Metainfo.TotalBytes() || (candidate.InfoHash.Valid && candidate.InfoHash.String != "" && candidate.InfoHash.String != c.Metainfo.InfoHash()) {
		return nil, ports.ErrAutomaticTVSubmissionReview
	}
	proof, e := automaticTVMakePrepared(a, work.SavePath, c.Metainfo)
	if e != nil {
		return nil, e
	}
	if _, e = q.CreateDownloadTask(ctx, db.CreateDownloadTaskParams{ID: c.TaskID, OperationID: a.OperationID, ClientID: a.Target.ClientID, CandidateID: a.CandidateID, InfoHash: proof.InfoHash, State: "submitting", SavePath: work.SavePath}); e != nil {
		return nil, e
	}
	raw := mustAutomaticTVJSON(proof)
	digest := proof.SHA256
	if e = q.AutomaticTVInsertSubmissionPreparation(ctx, db.AutomaticTVInsertSubmissionPreparationParams{OperationID: a.OperationID, TaskID: c.TaskID, PhysicalAttemptID: c.PhysicalAttemptID, JobID: c.Request.Lease.ID, LeaseOwner: c.Request.Lease.Owner, LeaseToken: c.Request.Lease.Token, AuthorityHash: a.DecisionStamp, InfoHash: proof.InfoHash, TorrentSha256: digest[:], ProofHash: completeTVJSONDigest(raw), Proof: raw}); e != nil {
		return nil, e
	}
	tag, e := tx.Exec(ctx, `UPDATE acquisition_reservation SET task_id=$2,updated_at=clock_timestamp() WHERE operation_id=$1 AND unit_id=$3 AND released_at IS NULL AND task_id IS NULL`, a.OperationID, c.TaskID, a.Target.UnitID)
	if e != nil {
		return nil, e
	}
	if tag.RowsAffected() != 1 {
		return nil, ports.ErrAutomaticTVSubmissionReview
	}
	tag, e = tx.Exec(ctx, `UPDATE unit_reservation SET state='dispatched',updated_at=clock_timestamp() WHERE operation_id=$1 AND unit_id=$2 AND root_id=$3 AND state='reserved' AND expires_at>clock_timestamp()`, a.OperationID, a.Target.UnitID, a.Target.RootID)
	if e != nil {
		return nil, e
	}
	if tag.RowsAffected() != 1 {
		return nil, ports.ErrAutomaticTVSubmissionReview
	}
	out = &ports.AutomaticTVSubmissionReceipt{TaskID: c.TaskID, State: "prepared_not_sent"}
	if e = s.automaticTVSubmissionEvent(ctx, q, a, "automatic_tv.submission_prepared", c.TaskID, mustAutomaticTVJSON(out)); e != nil {
		return nil, e
	}
	if e = automaticTVSubmissionProtections(ctx, tx, q, a, c.TaskID); e != nil {
		return nil, e
	}
	if e = automaticTVSubmissionPhysical(ctx, tx, q, c.Request, c.PhysicalAttemptID, true); e != nil {
		return nil, e
	}
	if e = automaticTVSubmissionTail(ctx, tx, q, c.Request, a, true); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return out, nil
}

func (s *AutomaticTVSubmissionStore) automaticTVSubmissionEvent(ctx context.Context, q *db.Queries, a ports.AutomaticTVExecutionAuthority, event string, task uuid.UUID, payload []byte) error {
	if _, e := q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: "system", ActorID: a.RoundID.String(), Action: event, TargetType: "download_task", TargetID: task.String(), Payload: payload}); e != nil {
		return e
	}
	_, e := (&AcquisitionStore{idgen: s.ids}).publishAcquisitionNotification(ctx, q, "acquisition", a.OperationID.String(), event, map[string]any{"operation_id": a.OperationID, "task_id": task, "result": json.RawMessage(payload)})
	return e
}
