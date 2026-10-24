package tmdb

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

const tvTranslationBody = `{"id":1396,"translations":[{"iso_639_1":"en","iso_3166_1":"US","data":{"name":"Breaking Bad","overview":"English plot"}},{"iso_639_1":"zh","iso_3166_1":"CN","data":{"name":"One Piece","overview":""}}]}`

func TestTVTranslationNativeExplicitLocale(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/tv/1396/translations" || r.URL.Query().Get("api_key") != "owned-key" || len(r.URL.Query()) != 1 {
			t.Error("translation request changed identity or locale inference")
		}
		_, _ = io.WriteString(w, tvTranslationBody)
	}))
	defer server.Close()
	client := NewClient("owned-key", WithBaseURL(server.URL), WithHTTPClient(server.Client()))
	value, err := client.GetTVTranslation(t.Context(), 1396, "zh-CN")
	if err != nil || value == nil || value.TMDBID != 1396 || value.Language != "zh-CN" || value.Title != "One Piece" || value.Overview != "" || calls.Load() != 1 {
		t.Fatal("explicit locale confused with script heuristic", value, err)
	}
	if value, err := client.GetTVTranslation(t.Context(), 1396, "en-US"); value != nil || !errors.Is(err, ports.ErrRejected) || calls.Load() != 1 {
		t.Fatal("unsupported language invoked request", err)
	}
}
func TestTVTranslationMissingAndMalformedEvidence(t *testing.T) {
	for name, body := range map[string]string{
		"identity":     strings.Replace(tvTranslationBody, `1396`, `1397`, 1),
		"missing_list": `{"id":1396}`, "null_list": `{"id":1396,"translations":null}`,
		"duplicate_row":   `{"id":1396,"translations":[{"iso_639_1":"zh","iso_3166_1":"CN","data":{"name":"a","overview":"b"}},{"iso_639_1":"zh","iso_3166_1":"CN","data":{"name":"c","overview":"d"}}]}`,
		"duplicate_field": strings.Replace(tvTranslationBody, `"name":"One Piece"`, `"name":"One Piece","Name":"other"`, 1),
		"missing_field":   strings.Replace(tvTranslationBody, `,"overview":""`, "", 1),
		"null_field":      strings.Replace(tvTranslationBody, `"overview":""`, `"overview":null`, 1),
		"wrong_region":    strings.Replace(tvTranslationBody, `"CN"`, `"China"`, 1),
		"null_data":       strings.Replace(tvTranslationBody, `{"name":"One Piece","overview":""}`, `null`, 1),
		"trailing":        tvTranslationBody + `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			if value, err := detailFixture(t, body).GetTVTranslation(t.Context(), 1396, "zh-CN"); value != nil || !errors.Is(err, ports.ErrBadResponse) {
				t.Fatal("ambiguous missing-locale evidence accepted", err)
			}
		})
	}
	for _, body := range []string{`{"id":1396,"translations":[]}`, strings.ReplaceAll(tvTranslationBody, `"CN"`, `"TW"`)} {
		value, err := detailFixture(t, body).GetTVTranslation(t.Context(), 1396, "zh-CN")
		if err != nil || value == nil || value.Title != "" || value.Overview != "" {
			t.Fatal("absent locale confused with another region", value, err)
		}
	}
}
