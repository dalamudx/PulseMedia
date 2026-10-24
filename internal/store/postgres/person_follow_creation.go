package postgres

import (
	"context"
	"encoding/json"
	"reflect"

	domain "github.com/dalamudx/PulseMedia/internal/domain/personfollow"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

func NewPersonFollowCreationStore(pool *pgxpool.Pool, ids ports.IDGen, fence ports.PersonFollowCreationFence) *SubscriptionCreationStore {
	own := fence
	return &SubscriptionCreationStore{pool: pool, ids: ids, personFence: &own}
}

type personCreationBinding struct {
	item        db.PersonFollowSyncItem
	work        ports.PersonFollowSyncWork
	observation ports.PersonWork
}

func (s *SubscriptionCreationStore) personCreationBinding(ctx context.Context, q *db.Queries, a *ports.Actor, in ports.SubscriptionCreationInput, id ports.SubscriptionCreationIdentity) (out personCreationBinding, err error) {
	invalid := ports.ErrSubscriptionBindingsConflict
	f := s.personFence
	if f == nil || f.Lease.ID == uuid.Nil || f.Lease.Owner == "" || f.Lease.Token <= 0 || f.ConfigID == uuid.Nil || f.ConfigVersion <= 0 || f.RunID == uuid.Nil || f.ExecutionID == uuid.Nil || f.ItemID == uuid.Nil || !creationHash(id.RequestHash) {
		return out, invalid
	}
	if err = acquisitionCommandActor(a); err != nil {
		return out, err
	}
	evidence, err := q.ReadPersonFollowExecution(ctx, f.ExecutionID)
	if err != nil {
		return out, err
	}
	if evidence.JobID != f.Lease.ID || evidence.RunID != f.RunID || evidence.LeaseOwner != f.Lease.Owner || evidence.LeaseToken != f.Lease.Token || evidence.ReleasedAt.Valid {
		return out, invalid
	}
	execution := &PersonFollowExecution{lease: f.Lease, configID: f.ConfigID, runID: f.RunID, ownerID: uuid.MustParse(a.ID), executionID: f.ExecutionID, pid: evidence.BackendPid, work: ports.PersonFollowSyncWork{Actor: *a}}
	work, err := execution.authorize(ctx, q)
	if err != nil {
		return out, err
	}
	execution.work = work
	if work.Config.Version != f.ConfigVersion || work.Config.ID != f.ConfigID {
		return out, invalid
	}
	item, err := q.LockPersonFollowItem(ctx, f.ItemID)
	if err != nil {
		return out, err
	}
	if item.RunID != f.RunID || item.Ordinal < int32(work.Start) || item.Ordinal >= int32(work.Start+ports.PersonFollowPageSize) || (item.State != "pending" && item.State != "created") || !item.RootID.Valid {
		return out, invalid
	}
	source, err := execution.readSource(ctx, q)
	if err != nil {
		return out, err
	}
	if source == nil || int(item.Ordinal) >= len(source.Works) {
		return out, invalid
	}
	observation := source.Works[item.Ordinal]
	root := work.Config.Settings.RootFor(observation.MediaType)
	var saved ports.PersonWork
	eligible, e := work.Config.Settings.Classify(observation)
	if e != nil || eligible != "pending" || root == uuid.Nil || item.RootID.Bytes != root || !creationStrictJSON(item.Source, &saved) || saved != observation || item.TmdbID != observation.TMDBID || item.MediaType != observation.MediaType || item.CommandKey != domain.CreationKey(f.ConfigID, f.ConfigVersion, root, observation) || id.Key != item.CommandKey {
		return out, invalid
	}
	if in.UserID != nil && *in.UserID != work.Config.OwnerID || in.MediaID != nil || in.TMDBID == nil || *in.TMDBID != observation.TMDBID || in.MediaType != observation.MediaType || in.RootID == nil || *in.RootID != root || in.QualityPolicyID != work.Config.Settings.QualityPolicyID || in.ScopeType != "all" || len(in.Seasons) != 0 || len(in.EpisodeKeys) != 0 || len(in.SelectedUnitIDs) != 0 || in.IncludeSpecials || in.ExpectedIntentVersion != nil {
		return out, invalid
	}
	var indexers []uuid.UUID
	var downloader *uuid.UUID
	if bindings := work.Config.Settings.Bindings; bindings != nil {
		indexers = bindings.IndexerIDs
		downloader = bindings.DownloaderID
	}
	if !reflect.DeepEqual(in.IndexerIDs, indexers) || !reflect.DeepEqual(in.DownloaderID, downloader) {
		return out, invalid
	}
	out = personCreationBinding{item: item, work: work, observation: observation}
	return out, nil
}
func (s *SubscriptionCreationStore) personCreationHistory(ctx context.Context, q *db.Queries, a *ports.Actor, in ports.SubscriptionCreationInput, id ports.SubscriptionCreationIdentity, b personCreationBinding) error {
	rows, err := q.ListPersonFollowCreatedCommand(ctx, id.Key)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		if b.item.State == "created" {
			return ports.ErrSubscriptionBindingsConflict
		}
		return nil
	}
	key, err := creationKey(id.Key)
	if err != nil {
		return err
	}
	owner := b.work.Config.OwnerID
	original, err := creationReceipt(ctx, q, a, key, id.RequestHash, &owner)
	if err != nil {
		return err
	}
	if original == nil {
		return ports.ErrSubscriptionBindingsConflict
	}
	if original.Subscription.RootID != *in.RootID || original.Subscription.UserID != owner {
		return ports.ErrSubscriptionBindingsConflict
	}
	for _, row := range rows {
		var receipt ports.SubscriptionCreationReceipt
		if row.ConfigID != b.work.Config.ID || row.ConfigVersion != b.work.Config.Version || row.OwnerID != owner || row.TmdbID != b.observation.TMDBID || row.MediaType != b.observation.MediaType || !row.RootID.Valid || row.RootID.Bytes != *in.RootID || !row.SubscriptionID.Valid || row.SubscriptionID.Bytes != original.Subscription.ID || !creationStrictJSON(row.Receipt, &receipt) || creationDigest(receipt) != creationDigest(original) {
			return ports.ErrSubscriptionBindingsConflict
		}
	}
	return nil
}
func (s *SubscriptionCreationStore) personCreationAuthorize(ctx context.Context, q *db.Queries, a *ports.Actor, in ports.SubscriptionCreationInput, id ports.SubscriptionCreationIdentity, prepared *ports.PreparedSubscriptionCreationMetadata, requirePrepared bool) error {
	b, err := s.personCreationBinding(ctx, q, a, in, id)
	if err != nil {
		return err
	}
	if err = s.personCreationHistory(ctx, q, a, in, id, b); err != nil {
		return err
	}
	if requirePrepared && (prepared == nil || prepared.TMDBID != b.observation.TMDBID || prepared.Media.MediaType != b.observation.MediaType) {
		return ports.ErrSubscriptionBindingsConflict
	}
	return nil
}
func (s *SubscriptionCreationStore) personCreationFinish(ctx context.Context, q *db.Queries, a *ports.Actor, in ports.SubscriptionCreationInput, id ports.SubscriptionCreationIdentity, receipt *ports.SubscriptionCreationReceipt) error {
	if err := lockAcquisitionUser(ctx, q, a); err != nil {
		return err
	}
	b, err := s.personCreationBinding(ctx, q, a, in, id)
	if err != nil {
		return err
	}
	if receipt == nil || receipt.Subscription.ID == uuid.Nil || receipt.Subscription.UserID != b.work.Config.OwnerID || receipt.Subscription.RootID != *in.RootID {
		return ports.ErrSubscriptionBindingsConflict
	}
	medium, tmdb, err := creationMedia(ctx, q, in)
	if err != nil {
		return err
	}
	if medium.ID != receipt.Subscription.MediaID || medium.MediaType != b.observation.MediaType || tmdb != b.observation.TMDBID {
		return ports.ErrSubscriptionBindingsConflict
	}
	if b.item.State == "created" {
		var stored ports.SubscriptionCreationReceipt
		if !b.item.SubscriptionID.Valid || b.item.SubscriptionID.Bytes != receipt.Subscription.ID || !creationStrictJSON(b.item.Receipt, &stored) || creationDigest(stored) != creationDigest(receipt) {
			return ports.ErrSubscriptionBindingsConflict
		}
	} else {
		raw, err := json.Marshal(receipt)
		if err != nil {
			return err
		}
		count, err := q.RecordPersonFollowCreation(ctx, db.RecordPersonFollowCreationParams{ID: b.item.ID, WriterExecutionID: s.personFence.ExecutionID, SubscriptionID: pgtype.UUID{Bytes: receipt.Subscription.ID, Valid: true}, Receipt: raw})
		if err != nil {
			return err
		}
		if count != 1 {
			return ports.ErrSubscriptionBindingsConflict
		}
	}
	if err = s.personCreationAuthorize(ctx, q, a, in, id, nil, false); err != nil {
		return err
	}
	return personFollowExecutionLease(ctx, q, s.personFence.Lease)
}
