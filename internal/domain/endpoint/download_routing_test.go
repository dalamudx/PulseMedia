package endpoint

import (
	"errors"
	"github.com/google/uuid"
	"slices"
	"testing"
)

func routeTestID(n byte) uuid.UUID {
	var id uuid.UUID
	id[6] = 0x40
	id[8] = 0x80
	id[15] = n
	return id
}

func TestDownloadRoutePrecedenceAndOrderIndependence(t *testing.T) {
	a, b, c, site := routeTestID(1), routeTestID(2), routeTestID(3), routeTestID(9)
	clients := []DownloadRouteClient{{a, true}, {b, true}, {c, true}}
	cases := []struct {
		name    string
		request DownloadRouteRequest
		rules   []DownloadRouteRule
		want    uuid.UUID
		basis   string
	}{
		{"default", DownloadRouteRequest{MediaType: "movie"}, []DownloadRouteRule{{DownloaderID: a}}, a, "default_rule"},
		{"movie", DownloadRouteRequest{MediaType: "movie"}, []DownloadRouteRule{{DownloaderID: a}, {MediaType: "movie", DownloaderID: b}, {MediaType: "tv", DownloaderID: c}}, b, "media_rule"},
		{"tv", DownloadRouteRequest{MediaType: "tv"}, []DownloadRouteRule{{MediaType: "movie", DownloaderID: b}, {MediaType: "tv", DownloaderID: c}}, c, "media_rule"},
		{"site_before_media", DownloadRouteRequest{MediaType: "movie", IndexerID: site}, []DownloadRouteRule{{MediaType: "movie", DownloaderID: a}, {IndexerID: site, DownloaderID: b}}, b, "site_rule"},
		{"site_and_media", DownloadRouteRequest{MediaType: "tv", IndexerID: site}, []DownloadRouteRule{{IndexerID: site, DownloaderID: a}, {MediaType: "tv", IndexerID: site, DownloaderID: c}, {MediaType: "tv", DownloaderID: b}}, c, "site_media_rule"},
		{"wrong_site", DownloadRouteRequest{MediaType: "tv", IndexerID: routeTestID(10)}, []DownloadRouteRule{{DownloaderID: b}, {IndexerID: site, DownloaderID: a}}, b, "default_rule"},
		{"unknown_site", DownloadRouteRequest{MediaType: "tv"}, []DownloadRouteRule{{DownloaderID: b}, {IndexerID: site, DownloaderID: a}}, b, "default_rule"},
		{"same_target_duplicates", DownloadRouteRequest{MediaType: "movie"}, []DownloadRouteRule{{MediaType: "movie", DownloaderID: a}, {MediaType: "movie", DownloaderID: a}}, a, "media_rule"},
		{"more_specific_overrides_conflict", DownloadRouteRequest{MediaType: "movie", IndexerID: site}, []DownloadRouteRule{{DownloaderID: a}, {DownloaderID: b}, {IndexerID: site, DownloaderID: c}}, c, "site_rule"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			originalRules, originalClients := slices.Clone(tc.rules), slices.Clone(clients)
			for reverse := 0; reverse < 2; reverse++ {
				rules, available := slices.Clone(tc.rules), slices.Clone(clients)
				if reverse == 1 {
					slices.Reverse(rules)
					slices.Reverse(available)
				}
				got, err := SelectDownloadRoute(tc.request, available, rules)
				if err != nil || got.DownloaderID != tc.want || got.Basis != tc.basis || got.PolicyVersion != 1 {
					t.Fatalf("unexpected decision: %v %+v", err, got)
				}
			}
			if !slices.Equal(tc.rules, originalRules) || !slices.Equal(clients, originalClients) {
				t.Fatal("selection mutated frozen inputs")
			}
		})
	}
}

func TestDownloadRouteManualBindingDoesNotReroute(t *testing.T) {
	a, b := routeTestID(1), routeTestID(2)
	for _, tc := range []struct {
		name    string
		clients []DownloadRouteClient
		id      uuid.UUID
		want    error
	}{
		{"manual_wins", []DownloadRouteClient{{a, true}, {b, true}}, a, nil},
		{"disabled_manual_not_replaced", []DownloadRouteClient{{a, false}, {b, true}}, a, ErrNoDownloadRouteClient},
		{"missing_manual_not_replaced", []DownloadRouteClient{{b, true}}, a, ErrNoDownloadRouteClient},
		{"nil_manual_id", []DownloadRouteClient{{b, true}}, uuid.Nil, ErrInvalidDownloadRoute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Malformed automatic policy cannot redirect an existing explicit selection.
			got, err := SelectDownloadRoute(DownloadRouteRequest{MediaType: "movie", ExplicitDownloaderID: &tc.id}, tc.clients, []DownloadRouteRule{{MediaType: "invalid"}})
			if !errors.Is(err, tc.want) {
				t.Fatalf("unexpected outcome: %v", err)
			}
			if tc.want == nil && (got.DownloaderID != a || got.Basis != "explicit") {
				t.Fatal("explicit intent changed")
			}
			if tc.want != nil && got != (DownloadRouteDecision{}) {
				t.Fatal("failure returned an executable selection")
			}
		})
	}
}

func TestDownloadRouteFailuresAndSingleFallback(t *testing.T) {
	a, b, site := routeTestID(1), routeTestID(2), routeTestID(9)
	for _, tc := range []struct {
		name    string
		media   string
		clients []DownloadRouteClient
		rules   []DownloadRouteRule
		want    error
	}{
		{"single", "movie", []DownloadRouteClient{{a, true}}, nil, nil},
		{"only_eligible", "tv", []DownloadRouteClient{{a, true}, {b, false}}, nil, nil},
		{"empty", "movie", nil, nil, ErrNoDownloadRouteClient},
		{"disabled", "tv", []DownloadRouteClient{{a, false}}, nil, ErrNoDownloadRouteClient},
		{"multiple_without_rules", "movie", []DownloadRouteClient{{a, true}, {b, true}}, nil, ErrAmbiguousDownloadRoute},
		{"same_scope_conflict", "movie", []DownloadRouteClient{{a, true}, {b, true}}, []DownloadRouteRule{{MediaType: "movie", DownloaderID: a}, {MediaType: "movie", DownloaderID: b}}, ErrAmbiguousDownloadRoute},
		{"unavailable_specific_no_fallback", "movie", []DownloadRouteClient{{a, true}, {b, false}}, []DownloadRouteRule{{DownloaderID: a}, {IndexerID: site, DownloaderID: b}}, ErrNoDownloadRouteClient},
		{"missing_specific_no_fallback", "movie", []DownloadRouteClient{{a, true}}, []DownloadRouteRule{{IndexerID: site, DownloaderID: b}}, ErrNoDownloadRouteClient},
		{"unknown_media", "series", []DownloadRouteClient{{a, true}}, nil, ErrInvalidDownloadRoute},
		{"empty_media", "", []DownloadRouteClient{{a, true}}, nil, ErrInvalidDownloadRoute},
		{"nil_client", "movie", []DownloadRouteClient{{uuid.Nil, true}}, nil, ErrInvalidDownloadRoute},
		{"duplicate_client", "movie", []DownloadRouteClient{{a, true}, {a, false}}, nil, ErrInvalidDownloadRoute},
		{"invalid_rule_media", "movie", []DownloadRouteClient{{a, true}}, []DownloadRouteRule{{MediaType: "series", DownloaderID: a}}, ErrInvalidDownloadRoute},
		{"nil_rule_target", "movie", []DownloadRouteClient{{a, true}}, []DownloadRouteRule{{MediaType: "movie"}}, ErrInvalidDownloadRoute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SelectDownloadRoute(DownloadRouteRequest{MediaType: tc.media, IndexerID: site}, tc.clients, tc.rules)
			if !errors.Is(err, tc.want) {
				t.Fatalf("unexpected outcome: %v", err)
			}
			if tc.want == nil && (got.DownloaderID != a || got.Basis != "single_client") {
				t.Fatal("single eligible fallback did not select its exact ID")
			}
			if tc.want != nil && got != (DownloadRouteDecision{}) {
				t.Fatal("failure returned an executable selection")
			}
		})
	}
}
