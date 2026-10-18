package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type upgradeProofClock struct{}

func (upgradeProofClock) Now() time.Time                  { return time.Now() }
func (upgradeProofClock) Since(t time.Time) time.Duration { return time.Since(t) }

type upgradeTestDispatchVerifier struct {
	payload ports.MovieUpgradeNativePayload
}

func (v upgradeTestDispatchVerifier) ConsumeMovieUpgradeDispatch(context.Context, ports.MovieUpgradeNativeDispatchProof, ports.MovieUpgradeNativeDescriptor) (ports.MovieUpgradeNativePayload, error) {
	return v.payload, nil
}

type upgradeProofClient struct {
	ports.DownloadClient
	calls *atomic.Int32
}

func (c upgradeProofClient) AddTorrent(context.Context, ports.AddTorrentParam) error {
	c.calls.Add(1)
	return nil
}

func TestMovieUpgradeNativeReturnProof(t *testing.T) {
	ctx := context.Background()
	raw := []byte("native-test-only-payload")
	sum := sha256.Sum256(raw)
	b := ports.MovieUpgradeSendReturnBinding{OperationID: uuid.New(), TaskID: uuid.New(), Owner: uuid.New(), Token: uuid.New(), ArmSHA256: string(make([]byte, 64)), RawSHA256: hex.EncodeToString(sum[:])}
	payload := ports.MovieUpgradeNativePayload{DispatchID: uuid.New(), Binding: b, TorrentBytes: raw}
	var calls, releases atomic.Int32
	a := &movieUpgradeNativeAttempt{driver: &movieUpgradeNativeDriver{verifier: upgradeTestDispatchVerifier{payload}, clock: upgradeProofClock{}}, descriptor: ports.MovieUpgradeNativeDescriptor{OperationID: b.OperationID, TaskID: b.TaskID, RawSHA256: b.RawSHA256}, client: upgradeProofClient{calls: &calls}, release: func() { releases.Add(1) }}
	proof, e := a.ExecuteMovieUpgradeSend(ctx, struct{}{})
	if e != nil {
		t.Fatal(e)
	}
	if v, e := a.VerifyMovieUpgradeSendReturn(ctx, proof, b); e != nil || v.Status != "confirmed" {
		t.Fatal(v, e)
	}
	copied := *(proof.(*movieUpgradeNativeReturn))
	copied.status = "unknown"
	encoded, e := json.Marshal(proof)
	if e != nil {
		t.Fatal(e)
	}
	var decoded any
	if e = json.Unmarshal(encoded, &decoded); e != nil {
		t.Fatal(e)
	}
	for _, claim := range []any{nil, map[string]any{"status": "confirmed"}, decoded, &copied} {
		if _, e = a.VerifyMovieUpgradeSendReturn(ctx, claim, b); e == nil {
			t.Fatal("forged return accepted")
		}
	}
	wrong := &movieUpgradeNativeAttempt{}
	if _, e = wrong.VerifyMovieUpgradeSendReturn(ctx, proof, b); e == nil {
		t.Fatal("cross-call return accepted")
	}
	changed := b
	changed.TaskID = uuid.New()
	if _, e = a.VerifyMovieUpgradeSendReturn(ctx, proof, changed); e == nil {
		t.Fatal("changed task binding accepted")
	}
	if _, e = a.ExecuteMovieUpgradeSend(ctx, struct{}{}); !errors.Is(e, ports.ErrMovieUpgradeSendObservation) || calls.Load() != 1 {
		t.Fatal("physical replay", e)
	}
	if e = a.Close(ctx); e != nil {
		t.Fatal(e)
	}
	if e = a.Close(ctx); e != nil || releases.Load() != 1 {
		t.Fatal("release was not once", e)
	}
	if _, e = a.VerifyMovieUpgradeSendReturn(ctx, proof, b); e == nil {
		t.Fatal("closed scope accepted return")
	}
}
