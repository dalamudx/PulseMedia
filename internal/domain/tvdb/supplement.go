package tvdb

import "errors"

var ErrEpisodeIdentityConflict = errors.New("conflicting episode identity")

type EpisodeIdentity struct {
	ID     int64
	Season int
	Number int
}

// SelectAdditions never fills numeric gaps or assigns a canonical ordinal.
// Only an identical provider identity may occupy an existing coordinate.
func SelectAdditions(season int, primary, observed []EpisodeIdentity) ([]int, error) {
	if season < 0 || season > 999 || len(primary) > 5000 || len(observed) > 64 {
		return nil, ErrEpisodeIdentityConflict
	}
	ids := map[int64]int{}
	numbers := map[int]int64{}
	for _, ep := range primary {
		if ep.ID <= 0 || ep.Season != season || ep.Number <= 0 || ep.Number > 9999 || ids[ep.ID] != 0 || numbers[ep.Number] != 0 {
			return nil, ErrEpisodeIdentityConflict
		}
		ids[ep.ID], numbers[ep.Number] = ep.Number, ep.ID
	}
	sourceIDs := map[int64]bool{}
	sourceNumbers := map[int]bool{}
	additions := []int{}
	for index, ep := range observed {
		if ep.ID <= 0 || ep.Season != season || ep.Number <= 0 || ep.Number > 9999 || sourceIDs[ep.ID] || sourceNumbers[ep.Number] {
			return nil, ErrEpisodeIdentityConflict
		}
		sourceIDs[ep.ID], sourceNumbers[ep.Number] = true, true
		if (ids[ep.ID] != 0 && ids[ep.ID] != ep.Number) || (numbers[ep.Number] != 0 && numbers[ep.Number] != ep.ID) {
			return nil, ErrEpisodeIdentityConflict
		}
		if ids[ep.ID] == 0 {
			additions = append(additions, index)
		}
	}
	return additions, nil
}
