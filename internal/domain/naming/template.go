package naming

import (
	"fmt"
	"strings"
)

// FormatNamingTemplate substitutes placeholders like {title}, {year}, {resolution}, {source} into final path.
func FormatNamingTemplate(templateStr string, vars map[string]string) (string, error) {
	if templateStr == "" {
		return "", fmt.Errorf("template cannot be empty")
	}

	result := templateStr
	for k, v := range vars {
		placeholder := fmt.Sprintf("{%s}", k)
		result = strings.ReplaceAll(result, placeholder, v)
	}

	// Clean relative path and verify safety
	clean, err := CleanRelPath(result)
	if err != nil {
		return "", fmt.Errorf("rendered template path unsafe: %w", err)
	}

	return clean, nil
}
