package personfollow

import (
	"bytes"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
)

var ErrInvalidSettings = errors.New("invalid person follow settings")

// Settings describe explicit future subscription intent, not an active subscription.
type Settings struct {
	PersonID              int64      `json:"person_id"`
	Role                  string     `json:"role"`
	Enabled               bool       `json:"enabled"`
	MinReleaseDate        string     `json:"min_release_date"`
	IntervalMinutes       int        `json:"interval_minutes"`
	MovieRootID           *uuid.UUID `json:"movie_root_id,omitempty"`
	TVRootID              *uuid.UUID `json:"tv_root_id,omitempty"`
	QualityPolicyID       string     `json:"quality_policy_id"`
	QualityPolicyRevision int64      `json:"quality_policy_revision"`
	Bindings              *Bindings  `json:"bindings,omitempty"`
}

type Bindings struct {
	IndexerIDs   []uuid.UUID `json:"indexer_ids"`
	DownloaderID *uuid.UUID  `json:"downloader_id,omitempty"`
}

func (s Settings) Validate() error {
	if s.PersonID <= 0 || (s.Role != "actor" && s.Role != "director") || s.IntervalMinutes < 15 || s.IntervalMinutes > 10080 || s.QualityPolicyRevision <= 0 || len(s.QualityPolicyID) == 0 || len(s.QualityPolicyID) > 128 {
		return ErrInvalidSettings
	}
	for _, b := range []byte(s.QualityPolicyID) {
		alnum := b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
		if !alnum && b != '_' && b != '-' && b != '.' {
			return ErrInvalidSettings
		}
	}
	date, err := time.Parse("2006-01-02", s.MinReleaseDate)
	if err != nil || len(s.MinReleaseDate) != 10 || date.Year() < 1800 {
		return ErrInvalidSettings
	}
	if s.MovieRootID != nil && *s.MovieRootID == uuid.Nil || s.TVRootID != nil && *s.TVRootID == uuid.Nil || s.Enabled && s.MovieRootID == nil && s.TVRootID == nil || s.MovieRootID != nil && s.TVRootID != nil && *s.MovieRootID == *s.TVRootID {
		return ErrInvalidSettings
	}
	if s.Bindings == nil {
		return nil
	}
	b := s.Bindings
	if b.IndexerIDs == nil || len(b.IndexerIDs) > 64 || b.DownloaderID != nil && *b.DownloaderID == uuid.Nil || len(b.IndexerIDs) > 0 && b.DownloaderID == nil || len(b.IndexerIDs) == 0 && b.DownloaderID != nil {
		return ErrInvalidSettings
	}
	seen := map[uuid.UUID]bool{}
	for _, id := range b.IndexerIDs {
		if id == uuid.Nil || seen[id] {
			return ErrInvalidSettings
		}
		seen[id] = true
	}
	return nil
}

func (s Settings) Clone() Settings {
	if s.MovieRootID != nil {
		id := *s.MovieRootID
		s.MovieRootID = &id
	}
	if s.TVRootID != nil {
		id := *s.TVRootID
		s.TVRootID = &id
	}
	if s.Bindings != nil {
		b := *s.Bindings
		b.IndexerIDs = slices.Clone(b.IndexerIDs)
		if b.DownloaderID != nil {
			id := *b.DownloaderID
			b.DownloaderID = &id
		}
		s.Bindings = &b
	}
	return s
}

// Normalized copies caller data and gives set-valued bindings a stable order.
// Nil bindings preserve the saved selection; an explicit empty list clears it.
func (s Settings) Normalized() Settings {
	s = s.Clone()
	if s.Bindings != nil {
		slices.SortFunc(s.Bindings.IndexerIDs, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	}
	return s
}
