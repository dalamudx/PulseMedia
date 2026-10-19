package postgres

import (
	"bytes"
	"encoding/json"
	"math"
	"path"
	"slices"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func automaticTVObservationRequestValid(r ports.AutomaticTVObservationRequest) bool {
	return r.OperationID != uuid.Nil && r.TaskID != uuid.Nil && r.PhysicalPermitID != uuid.Nil && r.Lease.ID != uuid.Nil && r.Lease.Owner != "" && r.Lease.Token > 0
}

func automaticTVObservationTarget(a ports.AutomaticTVExecutionAuthority) media.AutomaticTVTorrentTarget {
	t := a.Target
	return media.AutomaticTVTorrentTarget{Unit: media.NumberingUnit{ID: t.UnitID, EpisodeID: t.EpisodeID, Season: int(t.Season), Episode: int(t.Episode), Key: t.UnitKey, Version: t.UnitVersion}, Title: t.Title, OriginalTitle: t.OriginalTitle, Year: t.Year, TMDBID: t.TMDBID}
}

// The public snapshot deliberately has no JSON representation. Seal an explicit
// typed projection, with PostgreSQL UTC/microsecond normalization for authority.
func automaticTVObservationSeal(s ports.AutomaticTVObservationSnapshot, original string) string {
	a, err := automaticTVAuthorityBytes(s.Authority)
	if err != nil {
		return ""
	}
	raw, _ := json.Marshal([]any{"automatic-tv-observation-snapshot-v1", s.Request.OperationID, s.Request.TaskID, s.Request.Lease.ID, s.Request.Lease.Owner, s.Request.Lease.Token, s.Request.PhysicalPermitID, json.RawMessage(a), s.Authority.DecisionStamp, s.SendPhysicalAttemptID, s.ClientID, s.ClientStamp, s.InfoHash, s.OperationTag, s.SavePath, s.TotalBytes, s.MetainfoSHA256, s.Files, s.Video, original})
	return automaticTVDigest(raw)
}

func automaticTVObservationClone(s ports.AutomaticTVObservationSnapshot) *ports.AutomaticTVObservationSnapshot {
	s.Authority.Target.Rules = bytes.Clone(s.Authority.Target.Rules)
	original := s.Authority.Target.IndexerVersions
	s.Authority.Target.IndexerVersions = make(map[string]int64, len(original))
	for k, v := range original {
		s.Authority.Target.IndexerVersions[k] = v
	}
	s.Files = slices.Clone(s.Files)
	return &s
}

// Returns an exact known task separately from completion. A complete native task
// with contradictory file evidence is unknown, never progress-derived completion.
func automaticTVObservationEvidence(s ports.AutomaticTVObservationSnapshot, remote ports.AutomaticTVDownloadObservation) (known, complete bool, inventory []ports.AutomaticTVRemoteFile) {
	if !remote.Complete || remote.ClientID != s.ClientID || remote.ClientStamp != s.ClientStamp || remote.Before == nil || remote.After == nil || len(remote.Before) != 1 || len(remote.After) != 1 {
		return
	}
	a, b := remote.Before[0], remote.After[0]
	valid := func(t ports.AutomaticTVRemoteTask) bool {
		if t.InfoHash != s.InfoHash || t.SavePath != s.SavePath || t.TotalBytes != s.TotalBytes || t.TotalBytes <= 0 || t.VerifiedBytes < 0 || t.VerifiedBytes > t.TotalBytes || !slices.Contains(t.Tags, s.OperationTag) {
			return false
		}
		switch t.State {
		case "downloading", "seeding", "checking", "queued", "uploading", "stopped", "stalledUP", "pausedUP", "stoppedUP", "queuedUP", "forcedUP", "error", "missingFiles", "checkingUP", "allocating", "metaDL", "forcedMetaDL", "pausedDL", "stoppedDL", "queuedDL", "stalledDL", "checkingDL", "forcedDL", "checkingResumeData", "moving":
		default:
			return false
		}
		if t.ContentPath != "" && (!path.IsAbs(t.ContentPath) || path.Clean(t.ContentPath) != t.ContentPath || strings.ContainsAny(t.ContentPath, "\\\x00\r\n") || (t.ContentPath != s.SavePath && !strings.HasPrefix(t.ContentPath, strings.TrimRight(s.SavePath, "/")+"/"))) {
			return false
		}
		if t.Complete {
			if t.VerifiedBytes != t.TotalBytes {
				return false
			}
			switch t.State {
			case "uploading", "seeding", "stopped", "stalledUP", "pausedUP", "stoppedUP", "queuedUP", "forcedUP":
			default:
				return false
			}
		}
		return true
	}
	if !valid(a) || !valid(b) || a.InfoHash != b.InfoHash || a.SavePath != b.SavePath || a.ContentPath != b.ContentPath || a.TotalBytes != b.TotalBytes {
		return
	}
	if !a.Complete && !b.Complete {
		return true, false, nil
	}
	if !a.Complete || !b.Complete || a.VerifiedBytes != a.TotalBytes || b.VerifiedBytes != b.TotalBytes {
		return
	}
	if len(remote.Files) < 1 || len(remote.Files) > 4096 || len(remote.Files) != len(s.Files) {
		return
	}
	files := make([]media.TVTorrentFile, len(remote.Files))
	seen := make([]bool, len(remote.Files))
	expected := make(map[string]int64, len(s.Files))
	for _, f := range s.Files {
		expected[f.Path] = f.Size
	}
	var total int64
	for i, f := range remote.Files {
		if f.Index < 0 || f.Index >= len(seen) || seen[f.Index] || f.Size < 0 || f.CompletedBytes != f.Size || f.Size > math.MaxInt64-total {
			return false, false, nil
		}
		seen[f.Index] = true
		size, ok := expected[f.Path]
		if !ok || size != f.Size {
			return false, false, nil
		}
		delete(expected, f.Path)
		total += f.Size
		files[i] = media.TVTorrentFile{Path: f.Path, Size: f.Size}
	}
	if len(expected) != 0 || total != s.TotalBytes {
		return
	}
	video, err := media.ValidateAutomaticTVSingleEpisodeFiles(files, automaticTVObservationTarget(s.Authority))
	if err != nil || *video != s.Video {
		return
	}
	inventory = slices.Clone(remote.Files)
	slices.SortFunc(inventory, func(a, b ports.AutomaticTVRemoteFile) int {
		if a.Index < b.Index {
			return -1
		}
		if a.Index > b.Index {
			return 1
		}
		return 0
	})
	return true, true, inventory
}
