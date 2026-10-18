package postgres

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func (s *SubscriptionIntentStore) EnsureSubscriptionMedia(ctx context.Context, in ports.SubscriptionMediaInput, units []ports.SubscriptionMediaUnitSpec) (id uuid.UUID, err error) {
	defer func() { err = subscriptionIntentError(ctx, err) }()
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return uuid.Nil, e
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	t := &subscriptionIntentTx{tx: tx, q: db.New(tx), ids: s.ids}
	id, err = t.ensureMedia(ctx, in, units)
	if err != nil {
		return uuid.Nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

func (t *subscriptionIntentTx) ensureMedia(ctx context.Context, in ports.SubscriptionMediaInput, units []ports.SubscriptionMediaUnitSpec) (uuid.UUID, error) {
	if in.Namespace == "" || in.ExternalID == "" || len(units) == 0 {
		return uuid.Nil, ports.ErrInvalidSubscriptionBindings
	}
	if _, e := t.tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1 || ':' || $2, 0))", in.Namespace, in.ExternalID); e != nil {
		return uuid.Nil, e
	}
	existing, e := t.q.GetMediaByIdentity(ctx, db.GetMediaByIdentityParams{Namespace: in.Namespace, ExternalID: in.ExternalID})
	if e == nil {
		if existing.MediaType != in.MediaType {
			return uuid.Nil, ports.ErrInvalidSubscriptionBindings
		}
		return existing.ID, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return uuid.Nil, e
	}
	id := t.ids.NewUUID()
	var year pgtype.Int4
	if in.Year != nil {
		year = pgtype.Int4{Int32: *in.Year, Valid: true}
	}
	if _, e = t.q.CreateMedia(ctx, db.CreateMediaParams{ID: id, Title: in.Title, Year: year, MediaType: in.MediaType}); e != nil {
		return uuid.Nil, e
	}
	if _, e = t.q.CreateMediaIdentity(ctx, db.CreateMediaIdentityParams{ID: t.ids.NewUUID(), MediaID: id, Namespace: in.Namespace, ExternalID: in.ExternalID}); e != nil {
		return uuid.Nil, e
	}
	for _, u := range units {
		if u.SeasonNumber == nil && u.EpisodeNumber == nil {
			_, e = t.q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: t.ids.NewUUID(), MediaID: id, UnitKey: u.UnitKey})
		} else {
			var sn, en pgtype.Int4
			if u.SeasonNumber != nil {
				sn = pgtype.Int4{Int32: *u.SeasonNumber, Valid: true}
			}
			if u.EpisodeNumber != nil {
				en = pgtype.Int4{Int32: *u.EpisodeNumber, Valid: true}
			}
			_, e = t.q.CreateTVUnit(ctx, db.CreateTVUnitParams{ID: t.ids.NewUUID(), MediaID: id, UnitKey: u.UnitKey, SeasonNumber: sn, EpisodeNumber: en})
		}
		if e != nil {
			return uuid.Nil, e
		}
	}
	return id, nil
}
