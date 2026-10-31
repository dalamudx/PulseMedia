package postgres

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"math"
	"path/filepath"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type tvUpgradePrepared struct {
	SHA256   string                       `json:"sha256"`
	InfoHash string                       `json:"info_hash"`
	Total    int64                        `json:"total"`
	Files    []media.TVTorrentFile        `json:"files"`
	Video    media.TVTorrentUnitFile      `json:"video"`
	Target   media.TVEpisodeUpgradeTarget `json:"target"`
	SavePath string                       `json:"save_path"`
}

func (x *tvUpgradeSession) material(ctx context.Context) (*tvEpisodeUpgradeMaterial, error) {
	var m *tvEpisodeUpgradeMaterial
	e := x.transaction(ctx, true, func(tx pgx.Tx) error {
		var e error
		m, e = readTVEpisodeUpgradePreview(ctx, tx, x.actor, x.authority.Request, x.store.decoder)
		if e != nil {
			return e
		}
		return tvUpgradeProtection(ctx, tx, x.authority.OperationID, m.Unit.ID, m.Root.ID, x.actor.ID)
	})
	return m, e
}
func (x *tvUpgradeSession) inspect(ctx context.Context, m *tvEpisodeUpgradeMaterial) (*ports.AutomaticTVMetainfoProof, *tvUpgradePrepared, error) {
	if x.store.native == nil {
		return nil, nil, ports.ErrUnavailable
	}
	p, e := x.store.native.FetchTVEpisodeUpgrade(ctx, m.Source.Url, m.Candidate.LinkEncrypted, m.Source.ApiKeyEncrypted)
	if e != nil {
		return nil, nil, e
	}
	if p == nil || !p.ValidateBinding() {
		return nil, nil, ports.ErrTVEpisodeUpgradeConflict
	}
	video, e := media.ValidateTVEpisodeUpgradeTorrentFiles(p.Files(), m.Target)
	if e != nil {
		return nil, nil, ports.ErrTVEpisodeUpgradeConflict
	}
	if m.Candidate.InfoHash.Valid && strings.ToLower(m.Candidate.InfoHash.String) != p.InfoHash() {
		return nil, nil, ports.ErrTVEpisodeUpgradeConflict
	}
	digest := p.SHA256()
	save := filepath.Join(m.Root.Path, ".pulsemedia-upgrade", x.authority.OperationID.String())
	if !filepath.IsAbs(save) || filepath.Clean(save) != save {
		return nil, nil, ports.ErrTVEpisodeUpgradeConflict
	}
	return p, &tvUpgradePrepared{SHA256: hex.EncodeToString(digest[:]), InfoHash: p.InfoHash(), Total: p.TotalBytes(), Files: p.Files(), Video: *video, Target: m.Target, SavePath: save}, nil
}
func (x *tvUpgradeSession) prepare(ctx context.Context) (*ports.TVEpisodeUpgradeResult, error) {
	m, e := x.material(ctx)
	if e != nil {
		return nil, e
	}
	rootRelease, e := x.holdRoot(ctx, m)
	if e != nil {
		return nil, e
	}
	defer rootRelease()
	_, prepared, e := x.inspect(ctx, m)
	if e != nil {
		return nil, e
	}
	out := x.result("prepared_not_sent")
	e = x.transaction(ctx, true, func(tx pgx.Tx) error { return x.phase(ctx, tx, "prepare", out, prepared) })
	return out, e
}

type tvUpgradePermit struct {
	Owner, Token uuid.UUID
	Action       string
}

func (x *tvUpgradeSession) permit(ctx context.Context, action string, fresh bool) (*tvUpgradePermit, error) {
	p := &tvUpgradePermit{Owner: x.store.ids.NewUUID(), Token: x.store.ids.NewUUID(), Action: action}
	e := x.transaction(ctx, fresh, func(tx pgx.Tx) error {
		exists, e := db.New(tx).CheckTVEpisodeUpgradePermitExists(ctx, db.CheckTVEpisodeUpgradePermitExistsParams{OperationID: x.authority.OperationID, Action: action})
		if e != nil {
			return e
		}
		if exists {
			return ports.ErrTVEpisodeUpgradeObservation
		}
		e = db.New(tx).InsertTVEpisodeUpgradePermit(ctx, db.InsertTVEpisodeUpgradePermitParams{OperationID: x.authority.OperationID, Action: action, Owner: p.Owner, Token: p.Token, LockKey: x.lock, Nonce: x.nonce, RequestKey: x.key, RequestHash: x.hash})
		if e != nil {
			return e
		}
		return x.validPermit(ctx, tx, p)
	})
	return p, e
}
func (x *tvUpgradeSession) validPermit(ctx context.Context, tx pgx.Tx, p *tvUpgradePermit) error {
	valid, e := db.New(tx).CheckTVEpisodeUpgradePermit(ctx, db.CheckTVEpisodeUpgradePermitParams{OperationID: x.authority.OperationID, Action: p.Action, Owner: p.Owner, Token: p.Token, LockKey: x.lock, Nonce: x.nonce, RequestKey: x.key, RequestHash: x.hash})
	if e != nil {
		return e
	}
	if !valid {
		return ports.ErrTVEpisodeUpgradeConflict
	}
	return nil
}
func (x *tvUpgradeSession) recordReturn(ctx context.Context, p *tvUpgradePermit, status string, out *ports.TVEpisodeUpgradeResult, evidence any, fresh bool, finish func(pgx.Tx) error) error {
	return x.transaction(ctx, fresh, func(tx pgx.Tx) error {
		if e := x.validPermit(ctx, tx, p); e != nil {
			return e
		}
		raw, e := json.Marshal(evidence)
		if e != nil {
			return e
		}
		if e = db.New(tx).InsertTVEpisodeUpgradePhysicalReturn(ctx, db.InsertTVEpisodeUpgradePhysicalReturnParams{OperationID: x.authority.OperationID, Action: p.Action, Owner: p.Owner, Token: p.Token, Status: status, Evidence: raw}); e != nil {
			return e
		}
		if finish != nil {
			if e = finish(tx); e != nil {
				return e
			}
		}
		if e = x.phase(ctx, tx, p.Action, out, evidence); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); e != nil {
			return e
		}
		return x.validPermit(ctx, tx, p)
	})
}
func (x *tvUpgradeSession) openClient(ctx context.Context, m *tvEpisodeUpgradeMaterial) (ports.TVEpisodeUpgradeClient, func(), error) {
	if x.store.native == nil {
		return nil, nil, ports.ErrUnavailable
	}
	return x.store.native.OpenTVEpisodeUpgradeClient(ctx, ports.TVEpisodeUpgradeNativeConfig{ClientID: m.Client.ID, ClientType: m.Client.ClientType, URL: m.Client.Url, Username: m.Client.Username, PasswordEncrypted: m.Client.PasswordEncrypted})
}
func (x *tvUpgradeSession) send(ctx context.Context) (*ports.TVEpisodeUpgradeResult, error) {
	m, e := x.material(ctx)
	if e != nil {
		return nil, e
	}
	rootRelease, e := x.holdRoot(ctx, m)
	if e != nil {
		return nil, e
	}
	defer rootRelease()
	var prepared tvUpgradePrepared
	if e = x.transaction(ctx, true, func(tx pgx.Tx) error { return x.evidence(ctx, tx, "prepare", &prepared) }); e != nil {
		return nil, e
	}
	proof, current, e := x.inspect(ctx, m)
	if e != nil {
		return nil, e
	}
	if coverageDigest(current) != coverageDigest(prepared) {
		return nil, ports.ErrTVEpisodeUpgradeConflict
	}
	client, release, e := x.openClient(ctx, m)
	if e != nil {
		return nil, e
	}
	defer release()
	p, e := x.permit(ctx, "send", true)
	if e != nil {
		return nil, e
	}
	if e = x.transaction(ctx, true, func(tx pgx.Tx) error { return x.validPermit(ctx, tx, p) }); e != nil {
		return nil, e
	}
	if e = x.checkRoot(ctx, m); e != nil {
		return nil, e
	}
	e = client.AddTorrent(ctx, ports.AddTorrentParam{TorrentBytes: proof.Bytes(), InfoHash: prepared.InfoHash, SavePath: prepared.SavePath, Tags: "pulsemedia,pulsemedia:op:" + x.authority.OperationID.String()})
	status, state := "confirmed", "tracking"
	if e != nil || ctx.Err() != nil {
		status, state = "unknown", "send_unknown_requires_observation"
	}
	out := x.result(state)
	record, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if e = x.checkRoot(record, m); e != nil {
		return nil, e
	}
	if e = x.recordReturn(record, p, status, out, map[string]any{"info_hash": prepared.InfoHash}, true, nil); e != nil {
		return nil, e
	}
	return out, nil
}

type tvUpgradeObservation struct {
	Prepared tvUpgradePrepared `json:"prepared"`
	Complete bool              `json:"complete"`
}

func (x *tvUpgradeSession) observe(ctx context.Context) (*ports.TVEpisodeUpgradeResult, error) {
	m, e := x.material(ctx)
	if e != nil {
		return nil, e
	}
	rootRelease, e := x.holdRoot(ctx, m)
	if e != nil {
		return nil, e
	}
	defer rootRelease()
	var prepared tvUpgradePrepared
	e = x.transaction(ctx, true, func(tx pgx.Tx) error {
		if e := x.evidence(ctx, tx, "prepare", &prepared); e != nil {
			return e
		}
		valid, e := db.New(tx).CheckTVEpisodeUpgradeObservationAllowed(ctx, x.authority.OperationID)
		if e != nil {
			return e
		}
		if !valid.Valid || !valid.Bool {
			return ports.ErrTVEpisodeUpgradeObservation
		}
		return nil
	})
	if e != nil {
		return nil, e
	}
	client, release, e := x.openClient(ctx, m)
	if e != nil {
		return nil, e
	}
	defer release()
	action := "observe:" + x.key
	p, e := x.permit(ctx, action, true)
	if e != nil {
		return nil, e
	}
	if e = x.transaction(ctx, true, func(tx pgx.Tx) error { return x.validPermit(ctx, tx, p) }); e != nil {
		return nil, e
	}
	complete, known := false, false
	tag := "pulsemedia:op:" + x.authority.OperationID.String()
	if e = x.checkRoot(ctx, m); e != nil {
		return nil, e
	}
	first, callErr := client.ListTorrents(ctx, tag)
	if callErr == nil && tvUpgradeNativeIdentity(first, prepared, tag) {
		known = true
		if tvUpgradeComplete(first[0]) {
			files, er := client.GetTorrentFiles(ctx, prepared.InfoHash)
			if er == nil && tvUpgradeManifest(files, prepared) {
				second, er := client.ListTorrents(ctx, tag)
				complete = er == nil && tvUpgradeNativeIdentity(second, prepared, tag) && tvUpgradeComplete(second[0])
			}
		}
	}
	if ctx.Err() != nil {
		known, complete = false, false
	}
	state, status := "observation_unknown", "unknown"
	if known {
		state, status = "tracking", "confirmed"
	}
	if complete {
		state = "organization_review_required"
	}
	out := x.result(state)
	record, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if e = x.checkRoot(record, m); e != nil {
		return nil, e
	}
	if e = x.recordReturn(record, p, status, out, tvUpgradeObservation{Prepared: prepared, Complete: complete}, true, nil); e != nil {
		return nil, e
	}
	return out, nil
}
func tvUpgradeNativeIdentity(rows []*ports.TorrentInfo, p tvUpgradePrepared, tag string) bool {
	if len(rows) != 1 || rows[0] == nil {
		return false
	}
	v := rows[0]
	found := false
	for _, t := range strings.Split(v.Tags, ",") {
		found = found || strings.TrimSpace(t) == tag
	}
	return found && strings.EqualFold(v.InfoHash, p.InfoHash) && v.SavePath == p.SavePath && v.TotalBytes == p.Total && v.Progress >= 0 && v.Progress <= 1 && !math.IsNaN(float64(v.Progress))
}
func tvUpgradeComplete(v *ports.TorrentInfo) bool {
	return v.Progress == 1 && v.DownloadedBytes == v.TotalBytes && (v.State == "completed" || v.State == "seeding" || v.State == "paused" || v.State == "stopped")
}
func tvUpgradeManifest(files []ports.TorrentFile, p tvUpgradePrepared) bool {
	if len(files) != len(p.Files) {
		return false
	}
	seen := map[string]bool{}
	indices := map[int32]bool{}
	for _, f := range files {
		if f.Index < 0 || indices[f.Index] || seen[f.Name] || f.Progress != 1 {
			return false
		}
		indices[f.Index] = true
		seen[f.Name] = true
		match := false
		for _, v := range p.Files {
			if f.Name == v.Path && f.SizeBytes == v.Size {
				match = true
				break
			}
		}
		if !match {
			return false
		}
	}
	return true
}
