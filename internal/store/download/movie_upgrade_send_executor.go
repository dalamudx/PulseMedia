package download

import (
	"context"
	"crypto/sha256"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type movieUpgradeSendExecutor struct {
	manager   *Manager
	work      ports.MovieUpgradeSendWorkRepository
	fetcher   ports.CompleteTVTorrentFetcher
	inspector ports.MovieUpgradeMetainfoInspector
	native    ports.MovieUpgradeNativeJobSendRepository
}

func NewMovieUpgradeSendJobExecutor(m *Manager, w ports.MovieUpgradeSendWorkRepository, f ports.CompleteTVTorrentFetcher, i ports.MovieUpgradeMetainfoInspector, n ports.MovieUpgradeNativeJobSendRepository) (ports.MovieUpgradeSendJobExecutor, error) {
	if m == nil || w == nil || f == nil || i == nil || n == nil {
		return nil, ports.ErrUnavailable
	}
	return &movieUpgradeSendExecutor{m, w, f, i, n}, nil
}
func (r *Repository) MovieUpgradeSendDriver(v ports.MovieUpgradeNativeDispatchVerifier, clock ports.Clock) (ports.MovieUpgradeNativeSendDriver, error) {
	return NewMovieUpgradeNativeSendDriver(r.execution, v, clock)
}
func (r *Repository) MovieUpgradeSendExecutor(w ports.MovieUpgradeSendWorkRepository, f ports.CompleteTVTorrentFetcher, i ports.MovieUpgradeMetainfoInspector, n ports.MovieUpgradeNativeJobSendRepository) (ports.MovieUpgradeSendJobExecutor, error) {
	return NewMovieUpgradeSendJobExecutor(r.execution, w, f, i, n)
}
func (e *movieUpgradeSendExecutor) ExecuteMovieUpgradeSendJob(ctx context.Context, req ports.MovieUpgradeSendJobRequest, actorID uuid.UUID) error {
	work, err := e.work.LoadMovieUpgradeSendWork(ctx, req, actorID)
	if err != nil {
		return err
	}
	if work == nil || work.Actor == nil || len(work.Request.ExpectedClaim) == 0 {
		return ports.ErrUnavailable
	}
	link, err := crypto.Decrypt(e.manager.masterKey, work.DownloadURLEncrypted)
	if err != nil {
		return ports.ErrUnavailable
	}
	defer clear(link)
	key, err := crypto.Decrypt(e.manager.masterKey, work.APIKeyEncrypted)
	if err != nil {
		return ports.ErrUnavailable
	}
	defer clear(key)
	// Synchronous, bounded fetch. Credentials/raw metainfo never enter a job or log.
	raw, err := e.fetcher.FetchCompleteTVTorrent(ctx, ports.CompleteTVTorrentSource{EndpointURL: work.EndpointURL, APIKey: string(key), DownloadURL: string(link)})
	if err != nil {
		return err
	}
	defer clear(raw)
	if sha256.Sum256(raw) != work.MetainfoSHA256 {
		return ports.ErrUpgradeReservationConflict
	}
	proof, err := e.inspector.InspectMovieUpgradeMetainfo(ctx, raw, work.Target)
	if err != nil {
		return err
	}
	// Accepted71 verifies native issuance, the exact original byte digest/list,
	// current authority and the frozen pre-fetch claim again before any Add.
	receipt, err := e.native.SendMovieUpgradeJob(ctx, work.Actor, work.Request, proof)
	if err != nil {
		return err
	}
	if receipt == nil {
		return ports.ErrUnavailable
	}
	return nil
}
