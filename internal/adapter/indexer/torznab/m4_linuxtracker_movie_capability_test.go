package torznab_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// This opt-in gate assesses only the single authorized LinuxTracker definition.
// A passing assessment with BLOCKED evidence never establishes search acceptance.
// No source is created and no public site is contacted by this gate.
func TestM4LinuxTrackerProductionMovieCapability(t *testing.T) {
	if os.Getenv("PULSE_M4_LINUXTRACKER_CAPABILITY") != "1" {
		t.Skip("explicit LinuxTracker production capability assessment required")
	}
	root := os.Getenv("PULSE_M4_SERVICES_DIR")
	if root == "" {
		t.Fatal("explicit owned M4 service directory required")
	}
	config := filepath.Join(root, "prowlarr", "config.xml")
	resolved, err := filepath.EvalSymlinks(config)
	if err != nil || resolved != config {
		t.Fatal("canonical non-symlink credential configuration required")
	}
	info, err := os.Lstat(config)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Fatal("regular 0600 credential configuration required before credential loading")
	}
	p := openM4Prowlarr(t)
	var definitions []struct {
		DefinitionName string `json:"definitionName"`
		Capabilities   *struct {
			SearchParams      *[]string `json:"searchParams"`
			MovieSearchParams *[]string `json:"movieSearchParams"`
			TVSearchParams    *[]string `json:"tvSearchParams"`
		} `json:"capabilities"`
	}
	if json.Unmarshal(p.read(t, "/api/v1/indexer/schema"), &definitions) != nil {
		t.Fatal("invalid bounded Prowlarr definition schema")
	}
	matches := 0
	for _, d := range definitions {
		if d.DefinitionName != "linuxtracker" {
			continue
		}
		matches++
		if d.Capabilities == nil {
			t.Fatal("LinuxTracker schema omits capabilities; compatibility unverified")
		}
		c := d.Capabilities
		if c.SearchParams == nil || c.MovieSearchParams == nil || c.TVSearchParams == nil {
			t.Fatal("LinuxTracker schema omits search parameter evidence; compatibility unverified")
		}
		// Parameter names are protocol metadata; arbitrary schema values stay private.
		for _, params := range [][]string{*c.SearchParams, *c.MovieSearchParams, *c.TVSearchParams} {
			for _, param := range params {
				if !slices.Contains([]string{"q", "imdbid", "imdbtitle", "imdbyear", "tmdbid", "tvdbid", "tvmazeid", "rid", "season", "ep", "year", "genre"}, param) {
					t.Fatal("unexpected capability parameter; raw value withheld")
				}
			}
		}
		t.Logf("M4_LINUXTRACKER_LOCAL_CAPABILITY searchParams=%q movieSearchParams=%q tvSearchParams=%q", *c.SearchParams, *c.MovieSearchParams, *c.TVSearchParams)
		if !slices.Contains(*c.MovieSearchParams, "q") {
			t.Log("M4_LINUXTRACKER_BLOCKED: installed definition lacks movie-search q required by production SearchPage t=movie; no source created, no public requests, no search/fetch acceptance")
		} else {
			t.Fatal("LinuxTracker advertises movie-search q; actual production caps/search/fetch still require separate authorized validation")
		}
	}
	if matches != 1 {
		t.Fatalf("expected one LinuxTracker definition, got %d", matches)
	}
}
