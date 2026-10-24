package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

type tvdbBrowseFixture struct {
	ports.MetadataProvider
	primary               *ports.TVDetails
	locale                *ports.TVTranslation
	primaryErr, localeErr error
	localeReads           int
}

func (f *tvdbBrowseFixture) GetTVDetails(context.Context, int64, string) (*ports.TVDetails, error) {
	return f.primary, f.primaryErr
}
func (f *tvdbBrowseFixture) GetTVTranslation(context.Context, int64, string) (*ports.TVTranslation, error) {
	f.localeReads++
	return f.locale, f.localeErr
}

type translationIdentity func(context.Context, int64) (ports.TVArtworkIdentity, error)

func (f translationIdentity) ResolveTVArtworkIdentity(ctx context.Context, id int64) (ports.TVArtworkIdentity, error) {
	return f(ctx, id)
}
func TestTVDBBrowseTranslationFailureAndPreservation(t *testing.T) {
	for _, scenario := range []string{"missing", "complete", "latin_title", "disabled", "unmapped", "bad_locale", "source_error", "late_disable", "storage_error", "primary_error", "wrong_identity"} {
		t.Run(scenario, func(t *testing.T) {
			settings := tvdbSettingsFixture()
			primary := &ports.TVDetails{TMDBID: 1396, Title: "Primary title", Overview: "primary overview", OriginalTitle: "Original", EpisodesCount: 2, SeasonsCount: 1, Seasons: []ports.TVSeasonDetails{{SeasonNumber: 1, EpisodeCount: 2}}}
			original := *primary
			fixture := &tvdbBrowseFixture{primary: primary, locale: &ports.TVTranslation{TMDBID: 1396, Language: "zh-CN"}}
			switch scenario {
			case "complete":
				fixture.locale.Title, fixture.locale.Overview = "中文名称", "已有简介"
			case "latin_title":
				fixture.locale.Title = "Primary title"
			case "disabled":
				settings.enabled = false
			case "bad_locale":
				fixture.localeErr = ports.ErrBadResponse
			case "storage_error":
				settings.fail = true
			case "primary_error":
				fixture.primaryErr = ports.ErrUnavailable
			}
			identity := translationIdentity(func(_ context.Context, id int64) (ports.TVArtworkIdentity, error) {
				if scenario == "unmapped" {
					return ports.TVArtworkIdentity{}, ports.ErrNotFound
				}
				if scenario == "wrong_identity" {
					id++
				}
				return ports.TVArtworkIdentity{TMDBID: id, TVDBID: 81189}, nil
			})
			calls := 0
			client := &http.Client{Transport: artworkTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Scheme != "https" || r.URL.Host != "api4.thetvdb.com" || r.URL.RawQuery != "" {
					t.Error("unsafe native destination")
				}
				body := `{"status":"success","data":{"token":"owned-token"}}`
				if r.URL.Path == "/v4/login" {
					var credentials map[string]string
					if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&credentials) != nil || credentials["apikey"] != "owned-api-key" || credentials["pin"] != "owned-pin" {
						t.Error("native login changed")
					}
				} else {
					if r.Method != http.MethodGet || r.URL.Path != "/v4/series/81189/translations/zho" || r.Header.Get("Authorization") != "Bearer owned-token" {
						t.Error("translation identity or language changed")
					}
					if scenario == "source_error" {
						return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
					}
					if scenario == "late_disable" {
						settings.change("disable")
					}
					body = `{"status":"success","data":{"language":"zho","name":"备用中文名","overview":"备用简介"}}`
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			browser := &tvdbBrowseProvider{MetadataProvider: fixture, translations: fixture, source: tvdbSource{settings: settings, httpClient: client, identity: identity}}
			result, err := browser.GetTVDetails(t.Context(), 1396, "zh-CN")
			if scenario == "primary_error" {
				if result != nil || !errors.Is(err, ports.ErrUnavailable) || calls != 0 || fixture.localeReads != 0 {
					t.Fatal("backup replaced failed canonical identity", err)
				}
				return
			}
			if err != nil || result == nil {
				t.Fatal("backup failure broke primary details", err)
			}
			if !reflect.DeepEqual(*primary, original) {
				t.Fatal("display fallback mutated canonical input")
			}
			if scenario == "missing" || scenario == "latin_title" {
				expected := original
				expected.Overview = "备用简介"
				if scenario == "missing" {
					expected.Title = "备用中文名"
				}
				source := result.TranslationSource
				copy := *result
				copy.TranslationSource = nil
				if !reflect.DeepEqual(copy, expected) || source == nil || source.SourceID != 81189 || source.Provider != "tvdb" || calls != 2 {
					t.Fatal("display supplement lost independent identity or primary facts", result, calls)
				}
				raw, _ := json.Marshal(result)
				if strings.Contains(string(raw), "owned-api-key") || strings.Contains(string(raw), "owned-pin") || strings.Contains(string(raw), "owned-token") {
					t.Fatal("browse response leaked credentials")
				}
			} else if !reflect.DeepEqual(*result, original) {
				t.Fatal("failed or unnecessary fallback replaced primary details")
			}
			expectedCalls := 0
			if scenario == "missing" || scenario == "latin_title" || scenario == "source_error" || scenario == "late_disable" {
				expectedCalls = 2
			}
			if calls != expectedCalls {
				t.Fatal("unexpected native request or retry", calls, expectedCalls)
			}
			if (scenario == "disabled" || scenario == "storage_error") && fixture.localeReads != 0 {
				t.Fatal("disabled fallback read locale upstream")
			}
		})
	}
}
