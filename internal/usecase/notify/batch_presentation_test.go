package notify

import (
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"strings"
	"testing"
)

func TestBatchPresentationVersionedCountsAndPrivacy(t *testing.T) {
	unit := uuid.New()
	p := ports.NotificationBatchPayload{Version: "notification.media.v1", MediaID: uuid.New(), Members: []ports.NotificationBatchMember{{SourceEventID: uuid.New(), UnitID: unit, RootID: uuid.New(), FileID: uuid.New()}, {SourceEventID: uuid.New(), UnitID: unit, RootID: uuid.New(), FileID: uuid.New()}}}
	raw, _ := json.Marshal(p)
	n := ports.Notification{EventID: uuid.New().String()}
	originalID := n.EventID
	if e := applyBatchPresentation(&n, p.MediaID.String(), raw); e != nil || !strings.Contains(n.Body, "1 个单元，2 个文件，2 个目标库") || n.EventID != originalID {
		t.Fatal(n, e)
	}
	for _, bad := range [][]byte{[]byte(`{"version":"notification.media.v1","secret":"private-credential"}`), append(append([]byte{}, raw...), []byte(" {}")...)} {
		if e := applyBatchPresentation(&n, p.MediaID.String(), bad); e == nil {
			t.Fatal("unvalidated external content")
		}
	}
	p.Members[1].SourceEventID = p.Members[0].SourceEventID
	raw, _ = json.Marshal(p)
	if e := applyBatchPresentation(&n, p.MediaID.String(), raw); e == nil {
		t.Fatal("duplicate source counted twice")
	}
}
