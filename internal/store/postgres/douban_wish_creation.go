package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NewDoubanWishCreationStore binds the ordinary creator to one durable page execution.
func NewDoubanWishCreationStore(pool *pgxpool.Pool, ids ports.IDGen, fence ports.DoubanWishCreationFence) *SubscriptionCreationStore {
	copied := fence
	return &SubscriptionCreationStore{pool: pool, ids: ids, wishFence: &copied}
}

func (s *SubscriptionCreationStore) wishCreationLockJob(ctx context.Context, q *db.Queries) error {
	f := s.wishFence
	if f == nil || f.Lease.ID == uuid.Nil || f.Lease.Owner == "" || f.Lease.Token < 1 || f.ConfigID == uuid.Nil || f.RunID == uuid.Nil || f.ItemID == uuid.Nil || f.ExecutionID == uuid.Nil || f.ConfigVersion < 1 {
		return ports.ErrSubscriptionBindingsConflict
	}
	isolated, err := q.GuardDoubanSyncIsolation(ctx)
	if err != nil {
		return err
	}
	if isolated {
		return ports.ErrUnavailable
	}
	j, err := q.LockDoubanWishCreationJob(ctx, f.Lease.ID)
	if err != nil {
		return err
	}
	if j.Kind != ports.DoubanWishSyncJobKind || j.State != "running" || !j.LeaseOwner.Valid || j.LeaseOwner.String != f.Lease.Owner || j.LeaseToken != f.Lease.Token {
		return ports.ErrSubscriptionBindingsConflict
	}
	return s.wishCreationLease(ctx, q)
}
func (s *SubscriptionCreationStore) wishCreationLease(ctx context.Context, q *db.Queries) error {
	f := s.wishFence
	ok, err := q.DoubanWishCreationLeaseValid(ctx, db.DoubanWishCreationLeaseValidParams{ID: f.Lease.ID, LeaseOwner: pgtype.Text{String: f.Lease.Owner, Valid: true}, LeaseToken: f.Lease.Token})
	if err != nil {
		return err
	}
	if !ok {
		return ports.ErrSubscriptionBindingsConflict
	}
	return nil
}

type wishCreationBinding struct {
	item   db.DoubanWishSyncItem
	source ports.DoubanWishItem
	match  ports.DoubanWishMatch
}

// The rows, rather than the factory DTO, carry source and identity authority.
func (s *SubscriptionCreationStore) wishCreationBinding(ctx context.Context, q *db.Queries, a *ports.Actor, in ports.SubscriptionCreationInput, id ports.SubscriptionCreationIdentity) (out wishCreationBinding, err error) {
	invalid := ports.ErrSubscriptionBindingsConflict
	f := s.wishFence
	if err = acquisitionCommandActor(a); err != nil {
		return out, err
	}
	c, err := q.LockDoubanWishCreationConfig(ctx, f.ConfigID)
	if err != nil {
		return out, err
	}
	owner := uuid.MustParse(a.ID)
	if c.ID != f.ConfigID || !c.Enabled || c.Version != f.ConfigVersion || c.OwnerID != owner || in.UserID != nil && *in.UserID != owner {
		return out, invalid
	}
	run, err := q.LockDoubanWishCreationRun(ctx, f.RunID)
	if err != nil {
		return out, err
	}
	if run.ConfigID != c.ID || run.ConfigVersion != c.Version || run.OwnerID != owner || run.Status != "queued" && run.Status != "running" {
		return out, invalid
	}
	page, err := q.LockDoubanWishCreationPage(ctx, f.Lease.ID)
	if err != nil {
		return out, err
	}
	if page.RunID != run.ID || page.Finished || !page.ExecutionID.Valid || page.ExecutionID.Bytes != f.ExecutionID || !page.ExecutionPid.Valid || page.ExecutionPid.Int32 <= 0 || !page.ExecutionOwner.Valid || page.ExecutionOwner.String != f.Lease.Owner || !page.ExecutionToken.Valid || page.ExecutionToken.Int64 != f.Lease.Token {
		return out, invalid
	}
	item, err := q.LockDoubanWishCreationItem(ctx, f.ItemID)
	if err != nil {
		return out, err
	}
	if item.RunID != run.ID || item.PageJobID != page.JobID || item.MediaType != page.MediaType || item.State != "matched" && item.State != "created" || item.CommandKey != id.Key || !creationHash(id.RequestHash) {
		return out, invalid
	}
	if !creationStrictJSON(item.Source, &out.source) || doubanWishDigest(out.source) != item.SourceHash || !wishNumeric(out.source.DoubanID) || out.source.DoubanID != item.DoubanID || out.source.MediaType != item.MediaType || out.source.Year < 1800 || out.source.Year > 9999 || len(out.source.Titles) == 0 || len(out.source.Titles) > 8 {
		return out, invalid
	}
	parsedID, parseErr := strconv.ParseUint(out.source.DoubanID, 10, 64)
	if parseErr != nil || parsedID == 0 || strconv.FormatUint(parsedID, 10) != out.source.DoubanID {
		return out, invalid
	}
	for _, title := range out.source.Titles {
		if media.WatchlistTitleKey(title) == "" {
			return out, invalid
		}
	}
	if !creationStrictJSON(item.Match, &out.match) || !item.MatchHash.Valid || doubanWishDigest(out.match) != item.MatchHash.String || out.match.State != "matched" || out.match.TMDBID <= 0 || out.match.MediaType != out.source.MediaType {
		return out, invalid
	}
	root := c.MovieRootID
	if item.MediaType == "tv" {
		root = c.TvRootID
	}
	if !root.Valid || in.RootID == nil || root.Bytes != *in.RootID || in.MediaID != nil || in.TMDBID == nil || *in.TMDBID != out.match.TMDBID || in.MediaType != out.match.MediaType || in.ScopeType != "all" || in.QualityPolicyID != "" && in.QualityPolicyID != "default" || in.IndexerIDs != nil || in.DownloaderID != nil || len(in.Seasons) != 0 || in.IncludeSpecials || len(in.SelectedUnitIDs) != 0 || len(in.EpisodeKeys) != 0 || in.ExpectedIntentVersion != nil {
		return out, invalid
	}
	stable := "douban-wish:" + uuid.NewSHA1(uuid.NameSpaceOID, []byte(c.ID.String()+":"+fmt.Sprint(c.Version)+":"+item.MediaType+":"+in.RootID.String()+":"+item.DoubanID)).String()
	if item.CommandKey != stable {
		return out, invalid
	}
	var observedPage ports.DoubanWishPage
	work := ports.DoubanWishSyncWork{RunID: run.ID, Config: doubanSyncConfig(c), Actor: *a, MediaType: page.MediaType, Start: int(page.PageStart), ExecutionID: f.ExecutionID}
	if !creationStrictJSON(page.Page, &observedPage) || !wishValidPage(work, observedPage) || !run.CanonicalID.Valid || run.CanonicalID.String != observedPage.CanonicalID || run.ProfileNumber.Valid && run.ProfileNumber.String != observedPage.ProfileNumber {
		return out, invalid
	}
	found := false
	for _, source := range observedPage.Items {
		if source.DoubanID == item.DoubanID && doubanWishDigest(source) == item.SourceHash {
			found = true
		}
	}
	if !found {
		return out, invalid
	}
	j, err := q.LockDoubanWishCreationJob(ctx, f.Lease.ID)
	if err != nil {
		return out, err
	}
	var payload doubanWishPayload
	if !creationStrictJSON(j.Payload, &payload) || payload.Version != 1 || payload.RunID != run.ID || payload.PageType != page.MediaType || payload.Start != int(page.PageStart) {
		return out, invalid
	}
	r, err := creationRoot(ctx, q, in, item.MediaType)
	if err != nil {
		return out, err
	}
	if r.ID != *in.RootID {
		return out, invalid
	}
	isolated, err := q.GuardDoubanSyncIsolation(ctx)
	if err != nil {
		return out, err
	}
	if isolated {
		return out, ports.ErrUnavailable
	}
	held, err := q.DoubanWishCreationPermitHeld(ctx, db.DoubanWishCreationPermitHeldParams{LockKey: doubanWishLockKey(c.ID), ExecutionPid: page.ExecutionPid.Int32})
	if err != nil {
		return out, err
	}
	if !held {
		return out, invalid
	}
	if err = s.wishCreationLockJob(ctx, q); err != nil {
		return out, err
	}
	out.item = item
	return out, nil
}
func (s *SubscriptionCreationStore) wishCreationAuthorize(ctx context.Context, q *db.Queries, a *ports.Actor, in ports.SubscriptionCreationInput, id ports.SubscriptionCreationIdentity, p *ports.PreparedSubscriptionCreationMetadata, requirePrepared bool) error {
	b, err := s.wishCreationBinding(ctx, q, a, in, id)
	if err != nil {
		return err
	}
	if err = s.wishCreationHistory(ctx, q, a, in, id, b); err != nil {
		return err
	}
	if !requirePrepared {
		return nil
	}
	if p == nil || p.TMDBID != b.match.TMDBID || p.Media.MediaType != b.source.MediaType || p.Media.Year == nil || int(*p.Media.Year) != b.source.Year {
		return ports.ErrSubscriptionBindingsConflict
	}
	title := media.WatchlistTitleKey(p.Media.Title)
	for _, alias := range b.source.Titles {
		if title != "" && title == media.WatchlistTitleKey(alias) {
			return nil
		}
	}
	return ports.ErrSubscriptionBindingsConflict
}

// Called after the original idempotency insert, or while recovering that exact
// historical result. The caller commits this ledger and creation in the same tx.
func (s *SubscriptionCreationStore) wishCreationFinish(ctx context.Context, q *db.Queries, a *ports.Actor, in ports.SubscriptionCreationInput, id ports.SubscriptionCreationIdentity, r *ports.SubscriptionCreationReceipt) error {
	if err := lockAcquisitionUser(ctx, q, a); err != nil {
		return err
	}
	b, err := s.wishCreationBinding(ctx, q, a, in, id)
	if err != nil {
		return err
	}
	if r == nil || r.Subscription.UserID != uuid.MustParse(a.ID) || r.Subscription.RootID != *in.RootID {
		return ports.ErrSubscriptionBindingsConflict
	}
	m, tmdb, err := creationMedia(ctx, q, in)
	if err != nil {
		return err
	}
	if m.ID != r.Subscription.MediaID || m.MediaType != b.source.MediaType || tmdb != b.match.TMDBID {
		return ports.ErrSubscriptionBindingsConflict
	}
	if b.item.State == "created" {
		var stored ports.SubscriptionCreationReceipt
		if !b.item.SubscriptionID.Valid || b.item.SubscriptionID.Bytes != r.Subscription.ID || !creationStrictJSON(b.item.Receipt, &stored) || creationDigest(stored) != creationDigest(r) {
			return ports.ErrSubscriptionBindingsConflict
		}
	} else {
		raw, err := json.Marshal(r)
		if err != nil {
			return err
		}
		count, err := q.RecordDoubanWishCreation(ctx, db.RecordDoubanWishCreationParams{ID: b.item.ID, SubscriptionID: pgtype.UUID{Bytes: r.Subscription.ID, Valid: true}, Receipt: raw})
		if err != nil {
			return err
		}
		if count != 1 {
			return ports.ErrSubscriptionBindingsConflict
		}
	}
	if err = lockAcquisitionUser(ctx, q, a); err != nil {
		return err
	}
	if err = s.wishCreationAuthorize(ctx, q, a, in, id, nil, false); err != nil {
		return err
	}
	// Keep the database wall-clock check as the final statement before COMMIT;
	// schema96 also rejects expiry in its deferred created-result constraint.
	return s.wishCreationLease(ctx, q)
}

// Closed creation history cannot authorize reconstructing a lost original result.
func (s *SubscriptionCreationStore) wishCreationHistory(ctx context.Context, q *db.Queries, a *ports.Actor, in ports.SubscriptionCreationInput, id ports.SubscriptionCreationIdentity, b wishCreationBinding) error {
	rows, err := q.ListDoubanWishCreatedCommand(ctx, id.Key)
	if err != nil {
		return err
	}
	invalid := ports.ErrSubscriptionBindingsConflict
	if len(rows) == 0 {
		if b.item.State == "created" {
			return invalid
		}
		return nil
	}
	owner := uuid.MustParse(a.ID)
	key, err := creationKey(id.Key)
	if err != nil {
		return err
	}
	original, err := creationReceipt(ctx, q, a, key, id.RequestHash, &owner)
	if err != nil {
		return err
	}
	if original == nil {
		return invalid
	}
	m, tmdb, err := creationMedia(ctx, q, in)
	if err != nil {
		return err
	}
	if original.Subscription.MediaID != m.ID || original.Subscription.RootID != *in.RootID || original.Subscription.UserID != owner || m.MediaType != b.source.MediaType || tmdb != b.match.TMDBID {
		return invalid
	}
	for _, row := range rows {
		if row.OwnerID != owner || row.ConfigID != s.wishFence.ConfigID || row.ConfigVersion != s.wishFence.ConfigVersion || row.DoubanID != b.source.DoubanID || row.MediaType != b.source.MediaType || !row.SubscriptionID.Valid || row.SubscriptionID.Bytes != original.Subscription.ID {
			return invalid
		}
		var source ports.DoubanWishItem
		var match ports.DoubanWishMatch
		var receipt ports.SubscriptionCreationReceipt
		if !creationStrictJSON(row.Source, &source) || doubanWishDigest(source) != row.SourceHash || source.DoubanID != row.DoubanID || source.MediaType != row.MediaType || source.Year < 1800 || source.Year > 9999 || len(source.Titles) == 0 || len(source.Titles) > 8 {
			return invalid
		}
		for _, title := range source.Titles {
			if media.WatchlistTitleKey(title) == "" {
				return invalid
			}
		}
		if !creationStrictJSON(row.Match, &match) || !row.MatchHash.Valid || doubanWishDigest(match) != row.MatchHash.String || match.State != "matched" || match.MediaType != row.MediaType || match.TMDBID != b.match.TMDBID {
			return invalid
		}
		if !creationStrictJSON(row.Receipt, &receipt) || creationDigest(receipt) != creationDigest(original) {
			return invalid
		}
	}
	return nil
}
