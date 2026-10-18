package postgres

import (
	"context"
	"encoding/json"
	"time"

	"fmt"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const preparationLeaseDuration = 60 * time.Second

type preparationLeaseRow struct {
	OperationID uuid.UUID `json:"operation_id"`
	ActorID     uuid.UUID `json:"actor_id"`
	Owner       uuid.UUID `json:"owner"`
	Token       uuid.UUID `json:"token"`
	State       string    `json:"state"`
	ClaimedAt   time.Time `json:"claimed_at"`
	LeaseUntil  time.Time `json:"lease_until"`
}

func decodePreparationLease(raw json.RawMessage) (preparationLeaseRow, error) {
	var l preparationLeaseRow
	if json.Unmarshal(raw, &l) != nil {
		return l, preparationConflict()
	}
	return l, nil
}

func preparationTablePK(table string) (string, bool) {
	switch table {
	case "movie_upgrade_preparation", "movie_upgrade_preparation_lease":
		return "operation_id", true
	case "download_task":
		return "id", true
	default:
		return "", false
	}
}

func preparationSameJSON(a, b json.RawMessage) bool {
	return completeTVJSONDigest(a) == completeTVJSONDigest(b)
}

func (b *preparationBasis) check(ctx context.Context, tx pgx.Tx) error {
	if err := b.own.verify(ctx, tx); err != nil {
		return fmt.Errorf("verify original protection rows: %w", err)
	}
	// Validate semantics again before applying the two owned INSERT effects.
	for table, rows := range b.own {
		switch table {
		case "acquisition_reservation", "unit_reservation", "download_requirement":
			for id := range rows {
				if err := upgradeValidateOwned(ctx, tx, table, id); err != nil {
					return fmt.Errorf("validate original protection rows: %w", err)
				}
			}
		}
	}
	var current json.RawMessage
	if err := tx.QueryRow(ctx, `
SELECT to_jsonb(a) FROM movie_upgrade_authority a WHERE operation_id=$1`,
		b.operation).Scan(&current); err != nil {
		return err
	}
	if !preparationSameJSON(current, b.authority) {
		return fmt.Errorf("authority changed: %w", preparationConflict())
	}
	for table, want := range b.rows {
		pk, ok := preparationTablePK(table)
		if !ok {
			return preparationConflict()
		}
		id := b.operation
		if table == "download_task" {
			var row struct {
				ID uuid.UUID `json:"id"`
			}
			if json.Unmarshal(want, &row) != nil || row.ID == uuid.Nil {
				return preparationConflict()
			}
			id = row.ID
		}
		if err := tx.QueryRow(ctx,
			"SELECT to_jsonb(x) FROM "+table+" x WHERE "+pk+"=$1",
			id).Scan(&current); err != nil {
			return err
		}
		if !preparationSameJSON(current, want) {
			return fmt.Errorf("prepared row %s changed: %w", table, preparationConflict())
		}
	}

	var tasks, jobs, preparations, lanes, reservations, requirements int
	err := tx.QueryRow(ctx, `
SELECT
 (SELECT count(*) FROM download_task WHERE operation_id=$1),
 (SELECT count(*) FROM job WHERE payload->>'operation_id'=$1::text),
 (SELECT count(*) FROM movie_upgrade_preparation WHERE operation_id=$1),
 (SELECT count(*) FROM acquisition_reservation WHERE operation_id=$1),
 (SELECT count(*) FROM unit_reservation WHERE operation_id=$1),
 (SELECT count(*) FROM download_requirement WHERE operation_id=$1)`,
		b.operation).Scan(
		&tasks, &jobs, &preparations, &lanes, &reservations, &requirements)
	if err != nil {
		return err
	}
	expectedJobs, err := b.checkSendJobBasis(ctx, tx)
	if err != nil {
		return err
	}
	if tasks != b.taskCount || jobs != expectedJobs || preparations != b.prepCount ||
		lanes != 1 || reservations != 1 || requirements != 1 {
		return fmt.Errorf("preparation cardinality changed: %w", preparationConflict())
	}
	// Schema21 increments the target facts revision once for this verified
	// download_task INSERT. Cardinality and complete task-row equality above
	// establish ownership before normalizing that single additional effect.
	fresh, now, _, err := upgradeEvidence(
		ctx, tx, b.actor, &b.snapshot.Target, b.operation, 2+b.taskCount, b.indexerDecoder)
	if err != nil {
		return err
	}
	if fresh != b.snapshot.ProtectedDigest || !b.snapshot.ValidUntil.After(now) {
		return fmt.Errorf("protected inputs changed or expired: %w", preparationConflict())
	}
	if raw, ok := b.rows["movie_upgrade_preparation_lease"]; ok {
		lease, err := decodePreparationLease(raw)
		if err != nil {
			return err
		}
		// The last check after SET CONSTRAINTS also enforces a live lease.
		if !lease.LeaseUntil.After(now) {
			return preparationConflict()
		}
	}
	return nil
}

func (b *preparationBasis) lockLease(
	ctx context.Context, tx pgx.Tx, c ports.PrepareMovieUpgrade,
) (preparationLeaseRow, error) {
	var raw json.RawMessage
	err := tx.QueryRow(ctx, `
SELECT to_jsonb(l) FROM movie_upgrade_preparation_lease l
WHERE operation_id=$1 FOR UPDATE`, b.operation).Scan(&raw)
	if err != nil {
		return preparationLeaseRow{}, preparationRequiredRow(err)
	}
	l, err := decodePreparationLease(raw)
	if err != nil {
		return l, err
	}
	var now time.Time
	if err = tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		return l, err
	}
	if l.OperationID != b.operation || l.ActorID != uuid.MustParse(b.actor.ID) ||
		l.Owner != c.Owner || l.Token != c.Token || l.State != "leased" ||
		!l.LeaseUntil.After(now) ||
		!l.LeaseUntil.Equal(l.ClaimedAt.Add(preparationLeaseDuration)) {
		return l, preparationConflict()
	}
	b.rows["movie_upgrade_preparation_lease"] = raw
	return l, b.check(ctx, tx)
}

// Missing required business rows are conflicts. Other DB errors retain their
// cause until upgradeError maps them to Unavailable or caller cancellation.
func preparationRequiredRow(err error) error {
	if err == pgx.ErrNoRows {
		return preparationConflict()
	}
	return err
}
