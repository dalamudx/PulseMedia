package httpapi

import "testing"

func TestSubscriptionListTMDBIdentity(t *testing.T) {
	for _, mediaType := range []string{"movie", "tv"} {
		for _, externalID := range []string{"1", "550", "9007199254740991"} {
			if id := subscriptionListTMDBID(mediaType, externalID); id == nil {
				t.Fatalf("canonical %s identity %q omitted", mediaType, externalID)
			}
		}
		for _, externalID := range []string{"", "0", "-1", "+550", "0550", " 550", "550 ", "550.0", "5e2", "５５０", "9007199254740992", "9223372036854775808", "00000000-0000-4000-8000-000000000550"} {
			if id := subscriptionListTMDBID(mediaType, externalID); id != nil {
				t.Fatalf("untrusted %s identity %q accepted", mediaType, externalID)
			}
		}
	}
	for _, mediaType := range []string{"", "multi", "person", "TV", "tv.legacy"} {
		if id := subscriptionListTMDBID(mediaType, "550"); id != nil {
			t.Fatalf("non-native type %q accepted", mediaType)
		}
	}
}
