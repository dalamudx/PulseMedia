package download

import (
	"context"
	"errors"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type automaticTVServices struct {
	repository ports.AutomaticTVSubmissionRepository
	fetcher    ports.AutomaticTVTorrentFetcher
	inspector  ports.TorrentMetainfoInspector
}

var _ ports.AutomaticTVSendJobExecutor = (*Manager)(nil)

func (m *Manager) SetAutomaticTVServices(repository ports.AutomaticTVSubmissionRepository, fetcher ports.AutomaticTVTorrentFetcher, inspector ports.TorrentMetainfoInspector) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.automaticTV = &automaticTVServices{repository: repository, fetcher: fetcher, inspector: inspector}
}

func (m *Manager) ExecuteAutomaticTVSendJob(ctx context.Context, request ports.AutomaticTVSendRequest) error {
	_, err := m.SendAutomaticTV(ctx, request)
	return err
}

// SendAutomaticTV owns the synchronous first invocation through its physical
// return. Any existing preparation requires observation of the original Add.
func (m *Manager) SendAutomaticTV(ctx context.Context, request ports.AutomaticTVSendRequest) (out *ports.AutomaticTVSubmissionResultReceipt, err error) {
	defer func() {
		if err == nil || errors.Is(err, ports.ErrAutomaticTVNeedsObservation) || errors.Is(err, ports.ErrAutomaticTVSubmissionReview) || errors.Is(err, ports.ErrAutomaticTVResultStale) {
			return
		}
		switch {
		case errors.Is(err, context.Canceled):
			err = context.Canceled
		case errors.Is(err, context.DeadlineExceeded):
			err = context.DeadlineExceeded
		default:
			err = ports.ErrUnavailable
		}
	}()
	if request.OperationID == uuid.Nil || request.Lease.ID == uuid.Nil || request.Lease.Owner == "" || request.Lease.Token <= 0 {
		return nil, ports.ErrAutomaticTVSubmissionReview
	}
	m.mu.RLock()
	services, factory := m.automaticTV, m.factory
	m.mu.RUnlock()
	if services == nil || services.repository == nil || services.fetcher == nil || services.inspector == nil || factory == nil {
		return nil, ports.ErrUnavailable
	}
	// A committed task is a permanent first-send fence, even after the job expires.
	if _, e := m.queries.GetDownloadTaskByOpID(ctx, request.OperationID); e == nil {
		return nil, ports.ErrAutomaticTVNeedsObservation
	} else if !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
	}
	// Reject unauthorized work before changing physical history. Prepare rechecks
	// current facts after fetching, under the original physical nonce.
	work, e := services.repository.LoadAutomaticTVSendWork(ctx, request)
	if e != nil {
		return nil, e
	}
	if work == nil || work.Request != request || work.Authority.OperationID != request.OperationID {
		return nil, ports.ErrAutomaticTVSubmissionReview
	}
	attempt, physicalRelease, e := m.acquireAutomaticTVPhysical(ctx, request)
	if e != nil {
		return nil, e
	}
	defer physicalRelease()
	release, e := m.acquireClientBudget(ctx, work.Authority.Target.ClientID)
	if e != nil {
		return nil, e
	}
	defer release()
	password, e := crypto.Decrypt(m.masterKey, work.ClientPasswordEncrypted)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	client, e := factory(work.ClientType, work.ClientURL, work.ClientUsername, string(password))
	if e != nil || client == nil {
		return nil, ports.ErrUnavailable
	}
	capable, ok := client.(interface{ AutomaticTVByteSender() ports.DownloadClient })
	if !ok {
		return nil, ports.ErrAutomaticTVSubmissionReview
	}
	client = capable.AutomaticTVByteSender()
	if client == nil {
		return nil, ports.ErrUnavailable
	}
	protector := crypto.NewSecretProtector(m.masterKey)
	apiKey, e := protector.DecryptSecret(work.SourceAPIKeyEncrypted)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	link, e := protector.DecryptSecret(work.DownloadURLEncrypted)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	raw, e := services.fetcher.FetchAutomaticTVTorrent(ctx, ports.AutomaticTVTorrentSource{EndpointURL: work.SourceEndpointURL, APIKey: string(apiKey), DownloadURL: string(link)})
	if e != nil {
		return nil, e
	}
	proof, e := ports.InspectAutomaticTVMetainfo(services.inspector, raw)
	if e != nil {
		return nil, ports.ErrAutomaticTVSubmissionReview
	}
	task := m.idgen.NewUUID()
	prepared, e := services.repository.PrepareAutomaticTVSubmission(ctx, ports.PrepareAutomaticTVSubmissionCommand{Request: request, TaskID: task, PhysicalAttemptID: attempt, Metainfo: proof})
	if e != nil {
		return nil, ports.ErrAutomaticTVNeedsObservation
	}
	if prepared == nil || prepared.AlreadyExisted || prepared.TaskID != task || prepared.State != "prepared_not_sent" {
		return nil, ports.ErrAutomaticTVNeedsObservation
	}
	persisted, e := m.queries.GetDownloadTaskByOpID(ctx, request.OperationID)
	if e != nil {
		return nil, ports.ErrAutomaticTVNeedsObservation
	}
	if persisted.ID != task || persisted.State != "submitting" || persisted.ClientID != work.Authority.Target.ClientID || persisted.InfoHash != proof.InfoHash() || persisted.SavePath != work.SavePath {
		return nil, ports.ErrAutomaticTVNeedsObservation
	}
	addErr := client.AddTorrent(ctx, ports.AddTorrentParam{TorrentBytes: proof.Bytes(), InfoHash: proof.InfoHash(), SavePath: work.SavePath, Tags: "pulsemedia,pulsemedia:op:" + request.OperationID.String()})
	if addErr == nil {
		addErr = ctx.Err()
	}
	// Even cancellation cannot release the physical nonce until real Add and this
	// bounded writeback return. An expired logical lease must retain submitting.
	resultCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	out, e = services.repository.RecordAutomaticTVSubmissionResult(resultCtx, ports.AutomaticTVSubmissionResultCommand{Request: request, TaskID: task, PhysicalAttemptID: attempt, Metainfo: proof, Acknowledged: addErr == nil, AddError: addErr})
	if e != nil {
		return nil, e
	}
	if out == nil || out.TaskID != task {
		return nil, ports.ErrAutomaticTVResultStale
	}
	if addErr != nil || out.State != "tracking" {
		return out, ports.ErrAutomaticTVNeedsObservation
	}
	return out, nil
}
