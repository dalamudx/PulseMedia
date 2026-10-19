package transfer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"path/filepath"
	"sort"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// Preparation proves original bytes and coverage. Permission is checked separately
// at every database boundary; SendValidUntil applies only to the original Add.
type automaticTVOrganizationProof struct {
	Kind       string                              `json:"kind"`
	Authority  ports.AutomaticTVExecutionAuthority `json:"authority"`
	SavePath   string                              `json:"save_path"`
	InfoHash   string                              `json:"info_hash"`
	SHA256     [32]byte                            `json:"sha256"`
	TotalBytes int64                               `json:"total_bytes"`
	Files      []media.TVTorrentFile               `json:"files"`
	Video      media.TVTorrentUnitFile             `json:"video"`
}

type automaticTVOrganizationFence struct {
	ports.DownloadDemandFence
	Goal            string    `json:"goal"`
	Kind            string    `json:"kind"`
	Authority       uuid.UUID `json:"authority"`
	ProtectedDigest string    `json:"protected_digest"`
}

func automaticTVOrganizationDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func automaticTVOrganizationJSON(raw []byte) ([]byte, error) {
	var value any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func sameAutomaticTVJSON(a, b []byte) bool {
	x, e := automaticTVOrganizationJSON(a)
	if e != nil {
		return false
	}
	y, e := automaticTVOrganizationJSON(b)
	return e == nil && bytes.Equal(x, y)
}

func automaticTVOrganizationAuthority(raw []byte, operation uuid.UUID) (ports.AutomaticTVExecutionAuthority, error) {
	var a ports.AutomaticTVExecutionAuthority
	if json.Unmarshal(raw, &a) != nil {
		return a, ErrReviewRequired
	}
	t := &a.Target
	rules, e := automaticTVOrganizationJSON(t.Rules)
	if e != nil {
		return a, ErrReviewRequired
	}
	t.Rules = rules
	t.AirDate = t.AirDate.UTC()
	t.ObservedAt = t.ObservedAt.UTC()
	a.IssuedAt = a.IssuedAt.UTC().Truncate(time.Microsecond)
	a.SendValidUntil = a.SendValidUntil.UTC().Truncate(time.Microsecond)
	stamp := a.DecisionStamp
	a.DecisionStamp = ""
	canonical, e := json.Marshal(a)
	a.DecisionStamp = stamp
	if e != nil || automaticTVOrganizationDigest(canonical) != stamp || a.SchemaVersion != "automatic-tv-execution-v1" || a.Kind != "automatic_standard_single_episode_v1" || a.OperationID != operation || operation == uuid.Nil || a.RoundID == uuid.Nil || a.QueryID == uuid.Nil || a.CandidateID == uuid.Nil || a.IndexerID == uuid.Nil || a.SendValidUntil.Sub(a.IssuedAt) != 5*time.Minute || len(a.RootPathDigest) != 64 || t.SchemaVersion != "automatic-tv-round-v1" || t.ParserVersion != media.EpisodeParserVersion || t.IdentityRuleVersion != media.AutomaticTVIdentityRuleVersion || t.OwnerID == uuid.Nil || t.SubscriptionID == uuid.Nil || t.WantedID == uuid.Nil || t.MediaID == uuid.Nil || t.RootID == uuid.Nil || t.ClientID == uuid.Nil || t.UnitID == uuid.Nil || t.MappingMode != "standard" || t.MappingRevision < 0 || t.UnitVersion < 1 || t.MediaVersion < 1 || t.Season < 1 || t.Episode < 1 || t.EpisodeID < 1 || t.UnitKey != media.FormatEpisodeUnitKey(int(t.Season), int(t.Episode)) {
		return a, ErrReviewRequired
	}
	return a, nil
}

func automaticTVOrganizationVideo(raw []byte, a ports.AutomaticTVExecutionAuthority, inventory []byte) (automaticTVOrganizationProof, error) {
	var p automaticTVOrganizationProof
	if json.Unmarshal(raw, &p) != nil || p.Kind != "automatic_tv_prepared_v1" || p.TotalBytes < 1 || len(p.InfoHash) != 40 || p.SHA256 == ([32]byte{}) || !filepath.IsAbs(p.SavePath) || filepath.Clean(p.SavePath) != p.SavePath {
		return p, ErrReviewRequired
	}
	if _, e := hex.DecodeString(p.InfoHash); e != nil {
		return p, ErrReviewRequired
	}
	frozen, _ := json.Marshal(a)
	original, _ := json.Marshal(p.Authority)
	if !sameAutomaticTVJSON(frozen, original) {
		return p, ErrReviewRequired
	}
	t := a.Target
	video, e := media.ValidateAutomaticTVSingleEpisodeFiles(p.Files, media.AutomaticTVTorrentTarget{Unit: media.NumberingUnit{ID: t.UnitID, EpisodeID: t.EpisodeID, Season: int(t.Season), Episode: int(t.Episode), Key: t.UnitKey, Version: t.UnitVersion}, Title: t.Title, OriginalTitle: t.OriginalTitle, Year: t.Year, TMDBID: t.TMDBID})
	if e != nil || video == nil || *video != p.Video {
		return p, ErrReviewRequired
	}
	var native []ports.AutomaticTVRemoteFile
	if json.Unmarshal(inventory, &native) != nil || len(native) != len(p.Files) {
		return p, ErrReviewRequired
	}
	files := append([]media.TVTorrentFile(nil), p.Files...)
	observed := make([]media.TVTorrentFile, 0, len(native))
	indices := make(map[int]bool, len(native))
	var total int64
	for _, f := range native {
		if f.Index < 0 || f.Index >= len(native) || indices[f.Index] || f.Size < 0 || f.Size > math.MaxInt64-total || f.CompletedBytes != f.Size {
			return p, ErrReviewRequired
		}
		indices[f.Index] = true
		total += f.Size
		observed = append(observed, media.TVTorrentFile{Path: f.Path, Size: f.Size})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	sort.Slice(observed, func(i, j int) bool { return observed[i].Path < observed[j].Path })
	if total != p.TotalBytes {
		return p, ErrReviewRequired
	}
	for i := range files {
		if files[i] != observed[i] {
			return p, ErrReviewRequired
		}
	}
	return p, nil
}

func automaticTVOrganizationTarget(a ports.AutomaticTVExecutionAuthority, task uuid.UUID, inventoryStamp, source string, size int64) transferTargetEvidence {
	op := a.OperationID
	t := a.Target
	return transferTargetEvidence{AutomaticTVOperationID: &op, AutomaticTVInventoryStamp: inventoryStamp, ParserVersion: media.EpisodeParserVersion, UnitID: t.UnitID, UnitKey: t.UnitKey, UnitVersion: t.UnitVersion, MediaID: t.MediaID, MediaVersion: t.MediaVersion, InventoryTaskID: task, InventorySourcePath: source, InventorySizeBytes: size}
}

func automaticTVOrganizationTargetValid(a ports.AutomaticTVExecutionAuthority, f transferTargetEvidence) bool {
	t := a.Target
	return f.AutomaticTVOperationID != nil && *f.AutomaticTVOperationID == a.OperationID && f.EpisodeOperationID == uuid.Nil && f.CompleteTVPlanID == uuid.Nil && f.FileAction == nil && f.ParserVersion == media.EpisodeParserVersion && f.UnitID == t.UnitID && f.UnitKey == t.UnitKey && f.UnitVersion == t.UnitVersion && f.MediaID == t.MediaID && f.MediaVersion == t.MediaVersion && f.InventoryTaskID != uuid.Nil && len(f.AutomaticTVInventoryStamp) == 64 && f.InventorySizeBytes > 0 && filepath.IsAbs(f.InventorySourcePath)
}

func automaticTVOrganizationDemand(a ports.AutomaticTVExecutionAuthority) []byte {
	t := a.Target
	f := automaticTVOrganizationFence{DownloadDemandFence: ports.DownloadDemandFence{PolicyID: t.PolicyID, PolicyRevision: t.PolicyRevision, SubscriptionID: &t.SubscriptionID, IntentVersion: t.IntentVersion, RootGeneration: t.RootGeneration, UnitMetadataVersion: t.UnitVersion, MediaMetadataVersion: t.MediaVersion, CandidateID: a.CandidateID, RootID: t.RootID, UnitID: t.UnitID, MediaID: t.MediaID, ActorID: a.RoundID.String(), ActorType: ports.ActorTypeSystem, SourceStamp: a.SourceStamp}, Goal: "acquire", Kind: "automatic_tv_episode_v1", Authority: a.OperationID, ProtectedDigest: a.DecisionStamp}
	raw, _ := json.Marshal(f)
	return raw
}

func automaticTVOrganizationSource(mapping map[string]string, p automaticTVOrganizationProof) (string, error) {
	remote := filepath.Join(p.SavePath, filepath.FromSlash(p.Video.Path))
	local := naming.TranslateRemotePath(mapping, remote)
	if !filepath.IsAbs(local) || filepath.Clean(local) != local {
		return "", ports.ErrFileEscape
	}
	// Validate the original relative path independently of any prefix translation.
	if _, e := relative(p.SavePath, remote); e != nil {
		return "", e
	}
	return local, nil
}
