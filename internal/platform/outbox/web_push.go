package outbox

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

func (o *Outbox) publishImportedWebPush(ctx context.Context, q *db.Queries, event *db.OutboxEvent, file, unit uuid.UUID) error {
	devices, err := q.ListImportedWebPushDevices(ctx, db.ListImportedWebPushDevicesParams{FileID: file, UnitID: unit, VapidVersion: o.webPushVersion})
	if err != nil {
		return err
	}
	if len(devices) == 0 {
		return nil
	}
	input, err := q.GetImportedNotificationInput(ctx, db.GetImportedNotificationInputParams{FileID: file, UnitID: unit})
	if err != nil {
		return err
	}
	for _, device := range devices {
		delivery, err := q.InsertOutboxDelivery(ctx, db.InsertOutboxDeliveryParams{ID: o.idgen.NewUUID(), EventID: event.ID, Consumer: "webpush:" + device.ID.String(), AggregateType: event.AggregateType, AggregateID: event.AggregateID, Seq: event.Seq, MaxAttempts: 5})
		if err != nil {
			return err
		}
		if err = q.InsertWebPushDeliveryBinding(ctx, db.InsertWebPushDeliveryBindingParams{DeliveryID: delivery.ID, DeviceID: device.ID, UserID: device.UserID, VapidVersion: device.VapidVersion, UnitID: unit, FileID: file, RootID: input.RootID}); err != nil {
			return err
		}
	}
	return nil
}
