package mediaserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

type mediaCredentialDecoder struct{ key []byte }

func (d mediaCredentialDecoder) DecryptSecret(value []byte) ([]byte, error) {
	return crypto.Decrypt(d.key, value)
}

type refreshMutationClient struct{ mutate func() error }

func (c refreshMutationClient) RefreshPaths(context.Context, []string) error { return c.mutate() }
func (c refreshMutationClient) FindByPath(context.Context, string) (string, bool, error) {
	return "", false, nil
}
func (c refreshMutationClient) Test(context.Context) error { return nil }

func TestRefreshInjectedCredentialAndFrozenSnapshotPG(t *testing.T) {
	for _, change := range []string{"unchanged", "configuration", "file"} {
		t.Run(change, func(t *testing.T) {
			svc, ob, q, root, unit, file, server := setupSyncTest(t)
			ctx := context.Background()
			key := bytes.Repeat([]byte{11}, 32)
			encrypted, err := crypto.Encrypt(key, []byte("fixture-private-api-key"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = q.UpdateMediaServer(ctx, db.UpdateMediaServerParams{ID: server, Name: "fixture", ServerType: "jellyfin", Url: "http://localhost:8096", ApiKeyEncrypted: encrypted}); err != nil {
				t.Fatal(err)
			}
			svc.SetCredentialDecoder(mediaCredentialDecoder{key})
			factoryCalls := 0
			svc.SetServerFactory(func(kind, url, secret string) (ports.MediaServerClient, error) {
				factoryCalls++
				if kind != "jellyfin" || url != "http://localhost:8096" || secret != "fixture-private-api-key" {
					t.Error("codec did not decode existing master-key ciphertext")
				}
				return refreshMutationClient{mutate: func() error {
					switch change {
					case "configuration":
						_, e := q.UpdateMediaServer(ctx, db.UpdateMediaServerParams{ID: server, Name: "changed", ServerType: "jellyfin", Url: "http://localhost:8097", ApiKeyEncrypted: encrypted})
						return e
					case "file":
						return q.RetireFileByPath(ctx, db.RetireFileByPathParams{RootID: root, RelPath: "Fight Club (1999)/Fight Club.mkv"})
					}
					return nil
				}}, nil
			})
			event := uuid.New()
			payload, _ := json.Marshal(map[string]any{"file_id": file, "unit_id": unit})
			if _, err = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: event, AggregateType: "media", AggregateID: unit.String(), EventType: "media.imported", Payload: payload, Seq: 1}); err != nil {
				t.Fatal(err)
			}
			consumer := "mediaserver.refresh:" + server.String()
			if _, err = q.InsertOutboxDelivery(ctx, db.InsertOutboxDeliveryParams{ID: uuid.New(), EventID: event, Consumer: consumer, AggregateType: "media", AggregateID: unit.String(), Seq: 1, MaxAttempts: 5}); err != nil {
				t.Fatal(err)
			}
			claimed, err := ob.Claim(ctx, consumer, "snapshot", time.Minute)
			if err != nil || claimed == nil {
				t.Fatal("claim", err)
			}
			err = svc.ProcessRefreshDelivery(ctx, claimed.ID, server, "snapshot", claimed.LeaseToken, "")
			if factoryCalls != 1 {
				t.Fatal("connection factory did not run once", factoryCalls)
			}
			if change == "unchanged" && err != nil {
				t.Fatal(err)
			}
			if change != "unchanged" && err == nil {
				t.Fatal("stale snapshot completed refresh")
			}
			delivery, e := q.GetOutboxDeliveryByID(ctx, claimed.ID)
			if e != nil {
				t.Fatal(e)
			}
			if change == "unchanged" && delivery.State != "delivered" {
				t.Fatal("refresh not completed", delivery.State)
			}
			if change != "unchanged" && delivery.State == "delivered" {
				t.Fatal("stale snapshot marked delivered")
			}
		})
	}
}
