package httpapi

import (
	"errors"
	"testing"
)

func TestEndpointURLPreservationRequiresRevisionOnlyForLossyProjection(t *testing.T) {
	for _, tc := range []struct {
		name, current, submitted string
		present, matches         bool
		want                     string
		err                      error
	}{
		{"benign order", "https://example.test/?page=2&category=tv", "https://example.test/?page=2&category=tv", false, false, "https://example.test/?page=2&category=tv", nil},
		{"userinfo removed", "https://user:fixture@example.test/", "https://example.test/", true, true, "https://user:fixture@example.test/", nil},
		{"userinfo missing version", "https://user:fixture@example.test/", "https://example.test/", false, false, "", errEndpointRevision},
		{"fragment removed", "https://example.test/#fixture", "https://example.test/", true, true, "https://example.test/#fixture", nil},
		{"encoded mask changed", "https://example.test/?token=fixture", "https://other.test/?token=%2A%2A%2A", true, true, "", errEndpointMasked},
		{"full URL stale version", "https://example.test/?token=fixture", "http://127.0.0.1:8080/new", true, false, "", errEndpointRevision},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveEndpointEditURL(tc.submitted, tc.current, tc.present, tc.matches)
			if got != tc.want || !errors.Is(err, tc.err) {
				t.Fatalf("preservation result mismatch: %v", err)
			}
		})
	}
}
