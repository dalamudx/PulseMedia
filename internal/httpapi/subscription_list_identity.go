package httpapi

import "strconv"

// The list snapshot has already matched one native namespace to the actual media type.
func subscriptionListTMDBID(mediaType, externalID string) *int64 {
	if mediaType != "movie" && mediaType != "tv" {
		return nil
	}
	id, err := strconv.ParseInt(externalID, 10, 64)
	if err != nil || id <= 0 || id > 9007199254740991 || strconv.FormatInt(id, 10) != externalID {
		return nil
	}
	return &id
}
