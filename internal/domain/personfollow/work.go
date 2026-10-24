package personfollow

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const MaxWorks = 10000

var ErrInvalidWork = errors.New("invalid person work evidence")

// Work carries a provider identity and relationship observation, not execution authority.
type Work struct {
	TMDBID        int64
	MediaType     string
	Title         string
	OriginalTitle string
	ReleaseDate   string
}

func (w Work) Validate() error {
	if w.TMDBID <= 0 || (w.MediaType != "movie" && w.MediaType != "tv") || strings.TrimSpace(w.Title) == "" || len(w.Title) > 1024 || len(w.OriginalTitle) > 1024 || !utf8.ValidString(w.Title) || !utf8.ValidString(w.OriginalTitle) {
		return ErrInvalidWork
	}
	if w.ReleaseDate != "" {
		date, err := time.Parse("2006-01-02", w.ReleaseDate)
		if err != nil || len(w.ReleaseDate) != 10 || date.Year() < 1800 {
			return ErrInvalidWork
		}
	}
	return nil
}
func NormalizeWorks(input []Work) ([]Work, error) {
	if input == nil || len(input) > MaxWorks {
		return nil, ErrInvalidWork
	}
	result := make([]Work, 0, len(input))
	seen := make(map[struct {
		kind string
		id   int64
	}]Work, len(input))
	for _, work := range input {
		if err := work.Validate(); err != nil {
			return nil, err
		}
		key := struct {
			kind string
			id   int64
		}{work.MediaType, work.TMDBID}
		if previous, exists := seen[key]; exists {
			if previous != work {
				return nil, ErrInvalidWork
			}
			continue
		}
		seen[key] = work
		result = append(result, work)
	}
	slices.SortFunc(result, func(a, b Work) int {
		if n := strings.Compare(b.ReleaseDate, a.ReleaseDate); n != 0 {
			return n
		}
		if n := strings.Compare(a.MediaType, b.MediaType); n != 0 {
			return n
		}
		if a.TMDBID < b.TMDBID {
			return -1
		}
		if a.TMDBID > b.TMDBID {
			return 1
		}
		return 0
	})
	return result, nil
}
func (s Settings) RootFor(kind string) uuid.UUID {
	var root *uuid.UUID
	switch kind {
	case "movie":
		root = s.MovieRootID
	case "tv":
		root = s.TVRootID
	}
	if root == nil {
		return uuid.Nil
	}
	return *root
}

// Unknown release dates remain reviewable; they never stand in for the configured date.
func (s Settings) Classify(work Work) (string, error) {
	if s.Validate() != nil || !s.Enabled {
		return "", ErrInvalidSettings
	}
	if err := work.Validate(); err != nil {
		return "", err
	}
	if s.RootFor(work.MediaType) == uuid.Nil {
		return "excluded", nil
	}
	if work.ReleaseDate == "" {
		return "review_required", nil
	}
	if work.ReleaseDate < s.MinReleaseDate {
		return "excluded", nil
	}
	return "pending", nil
}
func CreationKey(config uuid.UUID, version int64, root uuid.UUID, work Work) string {
	if config == uuid.Nil || version <= 0 || root == uuid.Nil || work.Validate() != nil {
		return ""
	}
	identity := fmt.Sprintf("%s:%d:%s:%s:%d", config, version, root, work.MediaType, work.TMDBID)
	return "person-follow:" + uuid.NewSHA1(uuid.NameSpaceOID, []byte(identity)).String()
}
