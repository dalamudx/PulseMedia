package naming

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxNamingTemplateBytes = 4096

var ErrInvalidNamingTemplate = errors.New("invalid naming template")

// ValidateNamingTemplate validates syntax and literal paths without inventing
// missing metadata. Render-time fields must still be provided by the caller.
func ValidateNamingTemplate(template string) error {
	_, err := renderNamingTemplate(template, func(string) (string, bool) { return "value", true })
	return err
}

// FormatNamingTemplate renders each placeholder once. Values are components,
// never additional template source or directories. Output uses '/' on every OS.
func FormatNamingTemplate(template string, vars map[string]string) (string, error) {
	return renderNamingTemplate(template, func(name string) (string, bool) { value, ok := vars[name]; return value, ok })
}

func renderNamingTemplate(template string, lookup func(string) (string, bool)) (string, error) {
	if len(template) == 0 || len(template) > MaxNamingTemplateBytes || !utf8.ValidString(template) {
		return "", ErrInvalidNamingTemplate
	}
	var out strings.Builder
	for i := 0; i < len(template); {
		if template[i] == '}' {
			return "", ErrInvalidNamingTemplate
		}
		var piece string
		if template[i] == '{' {
			end := strings.IndexByte(template[i+1:], '}')
			if end < 0 {
				return "", ErrInvalidNamingTemplate
			}
			end += i + 1
			key := template[i+1 : end]
			if !namingTemplateField(key) {
				return "", ErrInvalidNamingTemplate
			}
			value, ok := lookup(key)
			if !ok || !namingTemplateValue(value) {
				return "", fmt.Errorf("%w: unavailable field %s", ErrInvalidNamingTemplate, key)
			}
			piece = value
			i = end + 1
		} else {
			end := i
			for end < len(template) && template[end] != '{' && template[end] != '}' {
				end++
			}
			piece = template[i:end]
			i = end
		}
		if out.Len()+len(piece) > MaxNamingTemplateBytes {
			return "", ErrInvalidNamingTemplate
		}
		out.WriteString(piece)
	}
	rendered := out.String()
	if !namingTemplatePath(rendered) {
		return "", ErrInvalidNamingTemplate
	}
	clean, err := CleanRelPath(rendered)
	if err != nil || filepath.ToSlash(clean) != rendered {
		return "", ErrInvalidNamingTemplate
	}
	return rendered, nil
}

func namingTemplateField(key string) bool {
	switch key {
	case "title", "year", "resolution", "source", "codec", "hdr", "audio", "edition", "group", "tmdb_id", "imdb_id", "season", "episode", "episode_key", "extension":
		return true
	default:
		return false
	}
}

func namingTemplateValue(value string) bool {
	return value != "" && len(value) <= 255 && utf8.ValidString(value) && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "/\\:\"<>|?*") && !strings.ContainsFunc(value, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) })
}

func namingTemplatePath(value string) bool {
	parts := strings.Split(value, "/")
	if len(parts) > 16 {
		return false
	}
	for _, part := range parts {
		if !namingTemplateValue(part) || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".") {
			return false
		}
		stem, _, _ := strings.Cut(strings.ToUpper(part), ".")
		switch stem {
		case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
			return false
		}
	}
	return true
}
