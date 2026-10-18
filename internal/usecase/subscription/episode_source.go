package subscription

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"sort"
	"time"
)

type EpisodeSourceService struct {
	repo     ports.EpisodeSourceRepository
	provider ports.TVEpisodeGroupProvider
}

func NewEpisodeSourceService(repo ports.EpisodeSourceRepository, provider ports.TVEpisodeGroupProvider) *EpisodeSourceService {
	return &EpisodeSourceService{repo, provider}
}
func (s *EpisodeSourceService) List(ctx context.Context, a *ports.Actor, id uuid.UUID) (*ports.EpisodeGroupList, error) {
	input, e := s.repo.ReadEpisodeSourceContext(ctx, a, id)
	if e != nil {
		return nil, e
	}
	if s.provider == nil {
		return nil, ports.ErrUnavailable
	}
	network, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	groups, e := s.provider.ListTVEpisodeGroups(network, input.TMDBID)
	if e != nil || groups == nil {
		return nil, ports.ErrUnavailable
	}
	current, e := s.repo.ReadEpisodeSourceContext(ctx, a, id)
	if e != nil {
		return nil, e
	}
	if current.InputStamp != input.InputStamp {
		return nil, ports.ErrEpisodeSourceConflict
	}
	return &ports.EpisodeGroupList{MediaID: input.MediaID, InputStamp: input.InputStamp, Groups: groups}, nil
}
func (s *EpisodeSourceService) Preview(ctx context.Context, a *ports.Actor, id uuid.UUID, groupID, partID string) (*ports.EpisodeGroupPreview, error) {
	input, e := s.repo.ReadEpisodeSourceContext(ctx, a, id)
	if e != nil {
		return nil, e
	}
	if s.provider == nil {
		return nil, ports.ErrUnavailable
	}
	network, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	source, e := s.provider.GetTVEpisodeGroup(network, input.TMDBID, groupID)
	if errors.Is(e, ports.ErrNotFound) {
		return nil, ports.ErrNotFound
	}
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	if source == nil || source.SeriesID != input.TMDBID || source.Summary.ID != groupID {
		return nil, ports.ErrUnavailable
	}
	current, e := s.repo.ReadEpisodeSourceContext(ctx, a, id)
	if e != nil {
		return nil, e
	}
	if current.InputStamp != input.InputStamp {
		return nil, ports.ErrEpisodeSourceConflict
	}
	copy := *source
	copy.Parts = append([]ports.TVEpisodeGroupPart(nil), source.Parts...)
	for i := range copy.Parts {
		copy.Parts[i].Episodes = append([]ports.TVEpisodeGroupEpisode(nil), copy.Parts[i].Episodes...)
	}
	source = &copy
	sort.Slice(source.Parts, func(i, j int) bool { return source.Parts[i].ID < source.Parts[j].ID })
	for i := range source.Parts {
		sort.Slice(source.Parts[i].Episodes, func(a, b int) bool { return source.Parts[i].Episodes[a].Order < source.Parts[i].Episodes[b].Order })
	}
	raw, _ := json.Marshal(source)
	sum := sha256.Sum256(raw)
	out := &ports.EpisodeGroupPreview{MediaID: input.MediaID, GroupID: groupID, PartID: partID, InputStamp: input.InputStamp, SourceHash: hex.EncodeToString(sum[:]), Rows: []ports.EpisodeGroupMappingRow{}, Source: *source}
	if partID == "" && len(source.Parts) == 1 {
		partID = source.Parts[0].ID
		out.PartID = partID
	}
	var part *ports.TVEpisodeGroupPart
	for i := range source.Parts {
		if source.Parts[i].ID == partID {
			part = &source.Parts[i]
			break
		}
	}
	if part == nil {
		out.HoldReason = "explicit_part_required"
		return out, nil
	}
	episodes := []media.NumberingEpisode{}
	contiguous := true
	for i, ep := range part.Episodes {
		episodes = append(episodes, media.NumberingEpisode{SourceNumber: ep.Order + 1, EpisodeID: ep.ID, Season: ep.Season, Episode: ep.Episode})
		if ep.Order != i {
			contiguous = false
		}
	}
	units := []media.NumberingUnit{}
	for _, u := range input.Units {
		units = append(units, media.NumberingUnit{ID: u.ID, EpisodeID: u.EpisodeID, Season: u.Season, Episode: u.Episode, Key: u.Key, Version: u.Version, Retired: u.Retired})
	}
	rows, reason := media.BuildEpisodeNumbering(episodes, units)
	out.HoldReason = reason
	if reason != "" {
		return out, nil
	}
	for _, r := range rows {
		out.Rows = append(out.Rows, ports.EpisodeGroupMappingRow{SourceNumber: r.SourceNumber, EpisodeID: r.EpisodeID, UnitID: r.UnitID, UnitKey: r.UnitKey, UnitVersion: r.UnitVersion})
	}
	out.CanMap = true
	out.CanUseAbsolute = source.Summary.Type == 2 && contiguous && len(source.Parts) == 1
	return out, nil
}
