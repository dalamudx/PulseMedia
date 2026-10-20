package mediasidecar

import (
	"context"
	"encoding/json"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SubtitleRepository shares only the imported-file snapshot and transaction
// fences. The new job kind, result and audit never change metadata receipts.
type SubtitleRepository struct{ base *Repository }

func NewSubtitleRepository(pool *pgxpool.Pool, ids ports.IDGen) *SubtitleRepository {
	return &SubtitleRepository{base: &Repository{pool: pool, ids: ids, kind: ports.MediaSubtitleJobKind}}
}

var _ ports.MediaSubtitleRepository = (*SubtitleRepository)(nil)

func (r *SubtitleRepository) LoadSubtitle(ctx context.Context, lease ports.JobLease) (ports.MediaSidecarSnapshot, error) {
	return r.base.Load(ctx, lease)
}

func (r *SubtitleRepository) ValidateSubtitle(ctx context.Context, lease ports.JobLease, expected ports.MediaSidecarSnapshot) error {
	return r.base.Validate(ctx, lease, expected)
}

func (r *SubtitleRepository) CompleteSubtitle(ctx context.Context, lease ports.JobLease, expected ports.MediaSidecarSnapshot, result ports.MediaSubtitleCompletion) error {
	b := r.base
	return b.transact(ctx, lease, func(q *db.Queries, payload ports.MediaSidecarPayload) error {
		if err := compare(ctx, q, lease, payload, expected, b.kind); err != nil {
			return err
		}
		review, err := validateSubtitleResult(expected, result)
		if err != nil {
			return err
		}
		if b.ids == nil {
			return ports.ErrUnavailable
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			return ports.ErrMediaSidecarResult
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return ports.ErrMediaSidecarResult
		}
		if _, err = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: b.ids.NewUUID(), ActorType: "system", ActorID: ports.MediaSubtitleJobKind, Action: "media.subtitle.completed", TargetType: "job", TargetID: lease.ID.String(), Payload: encoded}); err != nil {
			return err
		}
		_, err = q.CompleteMediaSidecarJob(ctx, db.CompleteMediaSidecarJobParams{ReviewRequired: review, Result: encoded, JobID: lease.ID, Owner: lease.Owner, Token: lease.Token, ExpectedPayload: body, Kind: b.kind})
		return staleOrUnavailable(err)
	})
}

func validateSubtitleResult(s ports.MediaSidecarSnapshot, c ports.MediaSubtitleCompletion) (bool, error) {
	bad := ports.ErrMediaSidecarResult
	zeroHash := c.VideoHash == (ports.SubtitleVideoHash{})
	zeroPublication := c.Publication == (ports.SidecarResult{})
	hashValid := c.VideoHash.SizeBytes == s.FileIdentity.Size && media.ValidShooterHash(c.VideoHash.Hash, c.VideoHash.SizeBytes)
	switch c.Outcome {
	case "too_small":
		if s.FileIdentity.Size < 0 || s.FileIdentity.Size >= ports.MinSubtitleVideoBytes || !zeroHash || c.Name != "" || !zeroPublication {
			return false, bad
		}
		return false, nil
	case "not_found":
		if !hashValid || c.Name != "" || !zeroPublication {
			return false, bad
		}
		return false, nil
	case "observed":
		name, err := media.ChineseSubtitleName(s.FileRelative)
		if err != nil || c.Name != name || (!zeroHash && !hashValid) {
			return false, bad
		}
	default:
		return false, bad
	}
	p, id := c.Publication, c.Publication.Identity
	valid := id.Device == s.RootIdentity.Device && id.Inode > 0 && id.Size >= 0
	if id != (ports.FileIdentity{}) && (!valid || (id.Device == s.FileIdentity.Device && id.Inode == s.FileIdentity.Inode) || (id.Device == s.RootIdentity.Device && id.Inode == s.RootIdentity.Inode)) {
		return false, bad
	}
	switch p.Disposition {
	case ports.SidecarCreated, ports.SidecarUncertain:
		if !hashValid || !p.Published || !valid || id.Size <= 0 || id.Size > ports.MaxSubtitleBytes {
			return false, bad
		}
		return p.Disposition == ports.SidecarUncertain, nil
	case ports.SidecarPreservedRegular:
		if p.Published || !valid {
			return false, bad
		}
		return false, nil
	case ports.SidecarNotPublished, ports.SidecarUnsafeExisting:
		if p.Published || id != (ports.FileIdentity{}) {
			return false, bad
		}
		return true, nil
	default:
		return false, bad
	}
}
