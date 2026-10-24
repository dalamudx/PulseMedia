package mediarequest

import (
	"errors"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

var ErrInvalid = errors.New("invalid media request")

const (
	Pending   = "pending"
	Approved  = "approved"
	Rejected  = "rejected"
	Withdrawn = "withdrawn"
)

// Intent carries a provider identity to verify later, never execution authority.
type Intent struct {
	MediaType       string `json:"media_type"`
	TMDBID          int64  `json:"tmdb_id"`
	Seasons         []int  `json:"seasons"`
	IncludeSpecials bool   `json:"include_specials"`
}

func CanonicalIntent(in Intent) (Intent, error) {
	in.Seasons = slices.Clone(in.Seasons)
	if in.MediaType != "movie" && in.MediaType != "tv" || in.TMDBID <= 0 || in.TMDBID > 9007199254740991 || len(in.Seasons) > 64 {
		return Intent{}, ErrInvalid
	}
	if in.MediaType == "movie" && (len(in.Seasons) > 0 || in.IncludeSpecials) {
		return Intent{}, ErrInvalid
	}
	slices.Sort(in.Seasons)
	for i, season := range in.Seasons {
		if season < 0 || season > 999 || season == 0 && !in.IncludeSpecials || i > 0 && season == in.Seasons[i-1] {
			return Intent{}, ErrInvalid
		}
	}
	if in.Seasons == nil {
		in.Seasons = []int{}
	}
	return in, nil
}
func Transition(state, action string) (string, error) {
	if state != Pending {
		return "", ErrInvalid
	}
	switch action {
	case "approve":
		return Approved, nil
	case "reject":
		return Rejected, nil
	case "withdraw":
		return Withdrawn, nil
	default:
		return "", ErrInvalid
	}
}
func CanonicalReason(reason string) (string, error) {
	if !utf8.ValidString(reason) || utf8.RuneCountInString(reason) > 512 {
		return "", ErrInvalid
	}
	for _, r := range reason {
		if unicode.IsControl(r) {
			return "", ErrInvalid
		}
	}
	return strings.TrimSpace(reason), nil
}
