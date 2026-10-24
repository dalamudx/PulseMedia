package postgres

import (
	"reflect"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestSubscriptionRecordTimestampRoundTrip(t *testing.T) {
	instant := time.Date(2026, 10, 24, 12, 34, 56, 123456000, time.UTC)
	for _, zone := range []*time.Location{time.FixedZone("host-utc", 0), time.FixedZone("east", 8*3600), time.FixedZone("west", -7*3600)} {
		t.Run(zone.String(), func(t *testing.T) {
			row := db.Subscription{
				IndexerIds: []byte("[]"), Seasons: []byte("[]"), SelectedUnitIds: []byte("[]"),
				CreatedAt: pgtype.Timestamptz{Time: instant.In(zone), Valid: true},
				UpdatedAt: pgtype.Timestamptz{Time: instant.Add(time.Second).In(zone), Valid: true},
			}
			record, err := subscriptionIntentRecord(row)
			if err != nil {
				t.Fatal(err)
			}
			if !record.CreatedAt.Equal(row.CreatedAt.Time) || !record.UpdatedAt.Equal(row.UpdatedAt.Time) {
				t.Fatal("timestamp conversion changed the persisted instant or precision")
			}
			if record.CreatedAt.Location() != time.UTC || record.UpdatedAt.Location() != time.UTC || !reflect.DeepEqual(record, creationClone(record)) {
				t.Fatal("the same subscription changes after the defensive JSON clone")
			}
			wanted := subscriptionIntentWanted(db.WantedItem{
				CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
				LastEvaluatedAt: row.CreatedAt, NextEvaluateAt: row.UpdatedAt,
			})
			if !wanted.CreatedAt.Equal(row.CreatedAt.Time) || !wanted.UpdatedAt.Equal(row.UpdatedAt.Time) || wanted.LastEvaluatedAt == nil || !wanted.LastEvaluatedAt.Equal(row.CreatedAt.Time) || wanted.NextEvaluateAt == nil || !wanted.NextEvaluateAt.Equal(row.UpdatedAt.Time) || wanted.ProjectionValidUntil != nil {
				t.Fatal("Wanted timestamp conversion changed known instants or invented an unknown value")
			}
			if !reflect.DeepEqual(wanted, creationClone(wanted)) {
				t.Fatal("Wanted changes after the defensive JSON clone")
			}
			changed := record
			changed.UpdatedAt = changed.UpdatedAt.Add(time.Microsecond)
			if reflect.DeepEqual(record, changed) {
				t.Fatal("different timestamp evidence became equal")
			}
		})
	}
}
