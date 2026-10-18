package download

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type trackingTestVerifier struct {
	binding ports.MovieUpgradeObservationBinding
}

func (v trackingTestVerifier) ConsumeMovieUpgradeTrackingQuery(context.Context, ports.MovieUpgradeTrackingQueryProof, ports.MovieUpgradeTrackingDescriptor) (ports.MovieUpgradeObservationBinding, error) {
	return v.binding, nil
}

type trackingTestClient struct {
	ports.DownloadClient
	identity ports.MovieUpgradeNativeObservationDescriptor
	calls    atomic.Int32
	entered  chan struct{}
	release  <-chan struct{}
}

func (c *trackingTestClient) ListTorrents(context.Context, string) ([]*ports.TorrentInfo, error) {
	if c.calls.Add(1) == 1 && c.entered != nil {
		close(c.entered)
		<-c.release
	}
	i := c.identity
	return []*ports.TorrentInfo{{InfoHash: i.InfoHash, SavePath: i.SavePath, Tags: i.OperationTag, TotalBytes: i.TotalBytes, DownloadedBytes: i.TotalBytes / 2, Progress: 0.5, State: "downloading"}}, nil
}
func (*trackingTestClient) GetTorrentMetainfo(context.Context, string) ([]byte, error) {
	return nil, ports.ErrBadResponse
}
func (*trackingTestClient) GetTorrentFiles(context.Context, string) ([]ports.TorrentFile, error) {
	return nil, ports.ErrBadResponse
}
func trackingTestAttempt(releases *atomic.Int32) (*movieUpgradeTrackingAttempt, *trackingTestClient, ports.MovieUpgradeObservationBinding) {
	binding := ports.MovieUpgradeObservationBinding{ObservationID: uuid.New(), OperationID: uuid.New(), TaskID: uuid.New(), Owner: uuid.New(), Token: uuid.New(), ObservationSHA256: strings.Repeat("a", 64), PermitSHA256: strings.Repeat("b", 64)}
	identity := ports.MovieUpgradeNativeObservationDescriptor{OperationID: binding.OperationID, TaskID: binding.TaskID, ClientID: uuid.New(), ClientDigest: strings.Repeat("c", 64), InfoHash: strings.Repeat("d", 40), OperationTag: "pulsemedia:op:" + binding.OperationID.String(), SavePath: "/downloads", TotalBytes: 10}
	client := &trackingTestClient{identity: identity}
	attempt := &movieUpgradeTrackingAttempt{driver: &movieUpgradeTrackingDriver{verifier: trackingTestVerifier{binding}, clock: upgradeProofClock{}}, descriptor: ports.MovieUpgradeTrackingDescriptor{Identity: identity}, client: client, release: func() { releases.Add(1) }}
	return attempt, client, binding
}
func TestMovieUpgradeNativeTrackingPrivateReturn(t *testing.T) {
	var releases atomic.Int32
	a, c, b := trackingTestAttempt(&releases)
	ctx := context.Background()
	proof, e := a.QueryMovieUpgradeTracking(ctx, struct{}{})
	if e != nil {
		t.Fatal(e)
	}
	out, e := a.VerifyMovieUpgradeTracking(ctx, proof, b)
	if e != nil || out.Outcome != "tracking" || out.ReturnedAt.IsZero() {
		t.Fatal(out, e)
	}
	copyProof := *proof.(*movieUpgradeTrackingReturn)
	for _, forgery := range []ports.MovieUpgradeTrackingProof{nil, struct{}{}, &copyProof} {
		if _, e = a.VerifyMovieUpgradeTracking(ctx, forgery, b); !errors.Is(e, ports.ErrUpgradeReservationConflict) {
			t.Fatal("accepted forged/copied return", e)
		}
	}
	other, _, _ := trackingTestAttempt(&releases)
	if _, e = other.VerifyMovieUpgradeTracking(ctx, proof, b); !errors.Is(e, ports.ErrUpgradeReservationConflict) {
		t.Fatal("accepted cross-attempt return", e)
	}
	changed := b
	changed.ObservationID = uuid.New()
	if _, e = a.VerifyMovieUpgradeTracking(ctx, proof, changed); !errors.Is(e, ports.ErrUpgradeReservationConflict) {
		t.Fatal("accepted cross-observation binding", e)
	}
	out.Evidence[0] = '['
	again, e := a.VerifyMovieUpgradeTracking(ctx, proof, b)
	if e != nil || again.Evidence[0] != '{' {
		t.Fatal("mutable native return evidence", e)
	}
	if _, e = a.QueryMovieUpgradeTracking(ctx, struct{}{}); e == nil || c.calls.Load() != 2 {
		t.Fatal("repeated native query", e, c.calls.Load())
	}
	if e = a.Close(ctx); e != nil || releases.Load() != 1 {
		t.Fatal(e, releases.Load())
	}
	if _, e = a.VerifyMovieUpgradeTracking(ctx, proof, b); e == nil {
		t.Fatal("closed attempt accepted proof")
	}
	_ = a.Close(ctx)
	if releases.Load() != 1 {
		t.Fatal("double budget release")
	}
	_ = other.Close(ctx)
}
func TestMovieUpgradeNativeTrackingCancellationHoldsBudget(t *testing.T) {
	var releases atomic.Int32
	a, c, b := trackingTestAttempt(&releases)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	c.entered = make(chan struct{})
	c.release = release
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type returned struct {
		proof ports.MovieUpgradeTrackingProof
		err   error
	}
	done := make(chan returned, 1)
	go func() { p, e := a.QueryMovieUpgradeTracking(ctx, struct{}{}); done <- returned{p, e} }()
	select {
	case <-c.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("query did not start")
	}
	cancel()
	if e := a.Close(context.Background()); !errors.Is(e, ports.ErrMovieUpgradeTrackingReview) || releases.Load() != 0 {
		t.Fatal("released active physical query", e, releases.Load())
	}
	unblock()
	var r returned
	select {
	case r = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("query did not drain")
	}
	if r.err != nil {
		t.Fatal(r.err)
	}
	if _, e := a.VerifyMovieUpgradeTracking(context.Background(), r.proof, b); e != nil {
		t.Fatal("returned proof lost after logical cancellation", e)
	}
	if e := a.Close(context.Background()); e != nil || releases.Load() != 1 {
		t.Fatal("budget not released once after physical return", e, releases.Load())
	}
}
