package media

import (
	"encoding/xml"
	"fmt"
)

type MovieNFOData struct {
	Title         string
	OriginalTitle string
	Year          int
	Plot          string
	TMDBID        int64
	IMDbID        string
	Genres        []string
}

type TVShowNFOData struct {
	Title         string
	OriginalTitle string
	Year          int
	Plot          string
	TMDBID        int64
}

type movieNFOXDoc struct {
	XMLName       xml.Name `xml:"movie"`
	Title         string   `xml:"title"`
	OriginalTitle string   `xml:"originaltitle,omitempty"`
	Year          int      `xml:"year,omitempty"`
	Plot          string   `xml:"plot,omitempty"`
	TMDBID        int64    `xml:"uniqueid"`
	IMDbID        string   `xml:"imdbid,omitempty"`
	Genres        []string `xml:"genre,omitempty"`
}

type tvShowNFOXDoc struct {
	XMLName       xml.Name `xml:"tvshow"`
	Title         string   `xml:"title"`
	OriginalTitle string   `xml:"originaltitle,omitempty"`
	Year          int      `xml:"year,omitempty"`
	Plot          string   `xml:"plot,omitempty"`
	TMDBID        int64    `xml:"uniqueid"`
}

// GenerateMovieNFO serializes movie metadata into Kodi/Jellyfin compatible XML (pure domain).
func GenerateMovieNFO(m MovieNFOData) ([]byte, error) {
	doc := movieNFOXDoc{
		Title:         m.Title,
		OriginalTitle: m.OriginalTitle,
		Year:          m.Year,
		Plot:          m.Plot,
		TMDBID:        m.TMDBID,
		IMDbID:        m.IMDbID,
		Genres:        m.Genres,
	}

	data, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal movie nfo: %w", err)
	}

	header := []byte(xml.Header)
	return append(header, data...), nil
}

// GenerateTVShowNFO serializes TV show metadata into Kodi/Jellyfin compatible XML (pure domain).
func GenerateTVShowNFO(tv TVShowNFOData) ([]byte, error) {
	doc := tvShowNFOXDoc{
		Title:         tv.Title,
		OriginalTitle: tv.OriginalTitle,
		Year:          tv.Year,
		Plot:          tv.Plot,
		TMDBID:        tv.TMDBID,
	}

	data, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal tvshow nfo: %w", err)
	}

	header := []byte(xml.Header)
	return append(header, data...), nil
}
