package media

import (
	"errors"
	"fmt"
	"strings"
)

type Type string

const (
	TypeMovie Type = "movie"
	TypeTV    Type = "tv"
)

// Identity represents a globally scoped external identifier (e.g. tmdb.movie:550).
type Identity struct {
	Namespace  string `json:"namespace"`
	ExternalID string `json:"external_id"`
}

func (i Identity) String() string {
	return fmt.Sprintf("%s:%s", i.Namespace, i.ExternalID)
}

func ParseIdentity(s string) (Identity, error) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return Identity{}, errors.New("invalid identity format (expected namespace:external_id)")
	}
	return Identity{
		Namespace:  strings.ToLower(parts[0]),
		ExternalID: parts[1],
	}, nil
}

// FormatMovieUnitKey returns the standard unit key for movies.
func FormatMovieUnitKey() string {
	return "movie"
}

// FormatEpisodeUnitKey returns standard episode unit keys (e.g. S01E03).
func FormatEpisodeUnitKey(season, episode int) string {
	return fmt.Sprintf("S%02dE%02d", season, episode)
}

// ParseEpisodeKey parses S01E03 or S00E01 format into season and episode numbers.
func ParseEpisodeKey(unitKey string) (season int, episode int, isSpecial bool, err error) {
	upper := strings.ToUpper(unitKey)
	var s, e int
	if n, err := fmt.Sscanf(upper, "S%02dE%02d", &s, &e); err == nil && n == 2 {
		return s, e, s == 0, nil
	}
	return 0, 0, false, fmt.Errorf("invalid episode unit key format: %s", unitKey)
}

func IsSpecialSeason(season int) bool {
	return season == 0
}
