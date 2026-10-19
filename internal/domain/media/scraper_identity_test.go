package media_test

import (
	"encoding/xml"
	"math"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
)

type decodedNFO struct {
	XMLName       xml.Name
	Title         string   `xml:"title"`
	OriginalTitle string   `xml:"originaltitle"`
	Plot          string   `xml:"plot"`
	Year          *int     `xml:"year"`
	IMDbID        string   `xml:"imdbid"`
	Genres        []string `xml:"genre"`
	IDs           []struct {
		Type    string `xml:"type,attr"`
		Default string `xml:"default,attr"`
		Value   int64  `xml:",chardata"`
	} `xml:"uniqueid"`
}

func TestNFOTrustedIdentityRoundTrip(t *testing.T) {
	text := `雪 & <uniqueid type="imdb">tt9999999</uniqueid> "quoted"`
	cases := []struct {
		name     string
		generate func() ([]byte, error)
	}{
		{"movie", func() ([]byte, error) {
			return media.GenerateMovieNFO(media.MovieNFOData{Title: text, OriginalTitle: text, Plot: text, TMDBID: math.MaxInt64})
		}},
		{"tvshow", func() ([]byte, error) {
			return media.GenerateTVShowNFO(media.TVShowNFOData{Title: text, OriginalTitle: text, Plot: text, TMDBID: math.MaxInt64})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := tc.generate()
			if err != nil {
				t.Fatal(err)
			}
			var doc decodedNFO
			if err := xml.Unmarshal(body, &doc); err != nil {
				t.Fatal(err)
			}
			if doc.XMLName.Local != tc.name || doc.Title != text || doc.OriginalTitle != text || doc.Plot != text {
				t.Fatalf("metadata did not round trip as XML text: %+v", doc)
			}
			if len(doc.IDs) != 1 || doc.IDs[0].Type != "tmdb" || doc.IDs[0].Default != "true" || doc.IDs[0].Value != math.MaxInt64 {
				t.Fatalf("TMDB identity was lost, narrowed or injected: %+v", doc.IDs)
			}
			if doc.Year != nil {
				t.Fatalf("unknown year fabricated: %d", *doc.Year)
			}
			if !strings.HasPrefix(string(body), xml.Header) {
				t.Fatal("XML declaration missing")
			}
		})
	}
}

func TestNFORejectsUntrustedIdentity(t *testing.T) {
	cases := []struct {
		name, title string
		id          int64
		year        int
	}{
		{"no_title", "", 550, 1999},
		{"blank_title", " \t\n\u3000", 550, 1999},
		{"unknown_identity", "Title", 0, 1999},
		{"negative_identity", "Title", -1, 1999},
		{"negative_year", "Title", 550, -1},
		{"invalid_year", "Title", 550, 10000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			movie, err := media.GenerateMovieNFO(media.MovieNFOData{Title: tc.title, TMDBID: tc.id, Year: tc.year})
			if err == nil || len(movie) != 0 {
				t.Fatal("invalid movie identity produced an NFO")
			}
			tv, err := media.GenerateTVShowNFO(media.TVShowNFOData{Title: tc.title, TMDBID: tc.id, Year: tc.year})
			if err == nil || len(tv) != 0 {
				t.Fatal("invalid show identity produced an NFO")
			}
		})
	}
}

func TestNFOKnownMovieMetadataRoundTrip(t *testing.T) {
	body, err := media.GenerateMovieNFO(media.MovieNFOData{Title: "Title", TMDBID: 550, Year: 1999, IMDbID: "tt0137523", Genres: []string{"Drama & Mystery", "<genre>Foreign</genre>"}})
	if err != nil {
		t.Fatal(err)
	}
	var doc decodedNFO
	if err := xml.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Year == nil || *doc.Year != 1999 || doc.IMDbID != "tt0137523" || len(doc.Genres) != 2 || doc.Genres[0] != "Drama & Mystery" || doc.Genres[1] != "<genre>Foreign</genre>" {
		t.Fatalf("known movie metadata changed: %+v", doc)
	}
}
