package postgres

import (
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type deliveryListCursor struct {
	Version   int       `json:"v"`
	State     string    `json:"state"`
	UpdatedAt time.Time `json:"at"`
	ID        uuid.UUID `json:"id"`
	EventID   string    `json:"event_id,omitempty"`
}

func decodeDeliveryListCursor(q ports.DeliveryListQuery) (deliveryListCursor, error) {
	var out deliveryListCursor
	if q.Limit < 1 || q.Limit > 100 || len(q.Cursor) > 512 {
		return out, ports.ErrInvalidDeliveryCommand
	}
	switch q.State {
	case "", "pending", "delivering", "delivered", "dead", "skipped":
	default:
		return out, ports.ErrInvalidDeliveryCommand
	}
	if q.Cursor == "" {
		return out, nil
	}
	raw, e := base64.RawURLEncoding.DecodeString(q.Cursor)
	if e != nil || base64.RawURLEncoding.EncodeToString(raw) != q.Cursor || json.Unmarshal(raw, &out) != nil || out.Version != 1 || out.State != q.State || out.EventID != deliveryEventScope(q.EventID) || out.UpdatedAt.IsZero() || out.ID == uuid.Nil {
		return out, ports.ErrInvalidDeliveryCommand
	}
	canonical, _ := json.Marshal(out)
	if string(canonical) != string(raw) {
		return out, ports.ErrInvalidDeliveryCommand
	}
	return out, nil
}
func deliveryEventScope(id uuid.UUID) string {
	if id == uuid.Nil {
		return ""
	}
	return id.String()
}
func encodeDeliveryListCursor(at time.Time, id uuid.UUID, query ports.DeliveryListQuery) string {
	raw, _ := json.Marshal(deliveryListCursor{Version: 1, State: query.State, UpdatedAt: at.UTC(), ID: id, EventID: deliveryEventScope(query.EventID)})
	return base64.RawURLEncoding.EncodeToString(raw)
}
