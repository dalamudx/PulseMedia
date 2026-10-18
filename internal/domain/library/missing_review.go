package library

// MissingReviewRequired pauses missing application for an abnormal complete scan.
// Division avoids overflow, while retaining the strict >20% boundary.
func MissingReviewRequired(missing, indexed int64) bool {
	if missing < 0 || indexed < 0 {
		return true
	}
	return missing >= 10 && missing > indexed/5
}
