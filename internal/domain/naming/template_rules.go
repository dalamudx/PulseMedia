package naming

import (
	"bytes"
	"encoding/json"
	"io"
)

const TemplateSettingKey = "organization.naming_templates"

// Empty templates preserve the existing canonical path rules for new plans.
// The setting row owns the version; an existing plan keeps its frozen target.
type TemplateRules struct {
	MovieTemplate   string `json:"movie_template"`
	EpisodeTemplate string `json:"episode_template"`
}

func ValidateTemplateRules(rules TemplateRules) error {
	for index, template := range []string{rules.MovieTemplate, rules.EpisodeTemplate} {
		if template == "" {
			continue
		}
		_, err := renderNamingTemplate(template, func(field string) (string, bool) {
			if index == 0 && (field == "season" || field == "episode" || field == "episode_key") {
				return "", false
			}
			return "value", true
		})
		if err != nil {
			return ErrInvalidNamingTemplate
		}
	}
	return nil
}

// DecodeTemplateRules rejects incomplete, duplicate or aliased declarations.
// In particular, a missing field cannot silently clear the other media type.
func DecodeTemplateRules(raw []byte) (TemplateRules, error) {
	var out TemplateRules
	if len(raw) > 64<<10 {
		return out, ErrInvalidNamingTemplate
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return out, ErrInvalidNamingTemplate
	}
	seen := make(map[string]bool, 2)
	for decoder.More() {
		token, err = decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] || (key != "movie_template" && key != "episode_template") {
			return TemplateRules{}, ErrInvalidNamingTemplate
		}
		seen[key] = true
		var value *string
		if decoder.Decode(&value) != nil || value == nil {
			return TemplateRules{}, ErrInvalidNamingTemplate
		}
		if key == "movie_template" {
			out.MovieTemplate = *value
		} else {
			out.EpisodeTemplate = *value
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') || len(seen) != 2 {
		return TemplateRules{}, ErrInvalidNamingTemplate
	}
	if _, err = decoder.Token(); err != io.EOF {
		return TemplateRules{}, ErrInvalidNamingTemplate
	}
	if err = ValidateTemplateRules(out); err != nil {
		return TemplateRules{}, err
	}
	return out, nil
}
