package naming_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/naming"
)

func TestNamingTemplateRulesStrictDeclaration(t *testing.T) {
	const valid = `{"movie_template":"{title}.{extension}","episode_template":"{title}/{episode_key}.{extension}"}`
	rules, err := naming.DecodeTemplateRules([]byte(valid))
	if err != nil || rules.MovieTemplate != "{title}.{extension}" || rules.EpisodeTemplate != "{title}/{episode_key}.{extension}" {
		t.Fatal("valid declaration rejected", err)
	}
	for _, raw := range []string{
		`{}`, `null`, `[]`, `"not an object"`,
		`{"movie_template":""}`, `{"movie_template":null,"episode_template":""}`,
		`{"movie_template":1,"episode_template":""}`,
		`{"Movie_template":"","episode_template":""}`,
		`{"movie_template":"","episode_template":"","execute":true}`,
		`{"movie_template":"","movie_template":"{title}.mkv","episode_template":""}`,
		`{"movie_template":"","movi\u0065_template":"{title}.mkv","episode_template":""}`,
		`{"movie_template":"","episode_template":"","episode_template":"{title}.mkv"}`,
		valid + `{}`, strings.Repeat(" ", 64<<10) + valid,
	} {
		if _, err = naming.DecodeTemplateRules([]byte(raw)); !errors.Is(err, naming.ErrInvalidNamingTemplate) {
			t.Fatal("incomplete or ambiguous naming declaration accepted")
		}
	}
}

func TestNamingTemplateRulesMediaScopeAndLegacyDefaults(t *testing.T) {
	raw, err := json.Marshal(naming.TemplateRules{})
	if err != nil {
		t.Fatal(err)
	}
	rules, err := naming.DecodeTemplateRules(raw)
	if err != nil || rules != (naming.TemplateRules{}) {
		t.Fatal("explicit default configuration changed", err)
	}
	for _, template := range []string{"{season}/{title}.mkv", "{title}.{episode}.mkv", "{title}/{episode_key}.mkv", "../{title}.mkv"} {
		if err = naming.ValidateTemplateRules(naming.TemplateRules{MovieTemplate: template}); !errors.Is(err, naming.ErrInvalidNamingTemplate) {
			t.Fatal("movie template acquired episode or escaping input")
		}
	}
	if err = naming.ValidateTemplateRules(naming.TemplateRules{EpisodeTemplate: "{title}/Season {season}/{episode_key}.{extension}"}); err != nil {
		t.Fatal(err)
	}
}
