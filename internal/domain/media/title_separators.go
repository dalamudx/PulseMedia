package media

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Dotted uppercase initials are explicit title punctuation, not generic separators.
var titleInitialsRegex = regexp.MustCompile(`(?:[A-Z]\.){1,8}[A-Z](?:\.|\b)`)

// Keep independently evidenced N.0 work versions, without treating year-sized
// integers or multi-digit fractions as decimal titles. Coverage reads raw tokens.
func normalizeTitleSeparators(s string) string {
	initialDots, initialEnds := map[int]bool{}, map[int]bool{}
	for _, span := range titleInitialsRegex.FindAllStringIndex(s, -1) {
		if span[0] > 0 {
			previous, _ := utf8.DecodeLastRuneInString(s[:span[0]])
			if unicode.IsLetter(previous) || unicode.IsDigit(previous) {
				continue
			}
		}
		if span[1] < len(s) && s[span[1]-1] != '.' {
			next, _ := utf8.DecodeRuneInString(s[span[1]:])
			if unicode.IsLetter(next) || unicode.IsDigit(next) {
				continue
			}
		}
		for j := span[0]; j < span[1]; j++ {
			if s[j] == '.' {
				initialDots[j] = true
			}
		}
		if s[span[1]-1] == '.' {
			initialEnds[span[1]-1] = true
		}
	}
	var out strings.Builder
	first := len(s) - len(strings.TrimLeftFunc(s, unicode.IsSpace))
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			if initialDots[i] {
				out.WriteByte('.')
				if initialEnds[i] && i+1 < len(s) {
					next, _ := utf8.DecodeRuneInString(s[i+1:])
					if unicode.IsLetter(next) || unicode.IsDigit(next) {
						out.WriteByte(' ')
					}
				}
				continue
			}
			start := i
			for start > 0 && s[start-1] >= '0' && s[start-1] <= '9' {
				start--
			}
			decimal := i-start >= 1 && i-start <= 3 && i+1 < len(s) && s[i+1] == '0' && (i+2 == len(s) || s[i+2] < '0' || s[i+2] > '9')
			spaced := i+1 < len(s) && (s[i+1] == ' ' || s[i+1] == '\t')
			ordinal := spaced && start == first && i-start >= 1 && i-start <= 3
			abbreviation := spaced && i-first == 2 && strings.EqualFold(s[first:i], "Mr")
			volume := spaced && i >= 3 && strings.EqualFold(s[i-3:i], "Vol") && (i == 3 || strings.ContainsRune(" ._([", rune(s[i-4])))
			if volume {
				j := i + 1
				for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
					j++
				}
				end := j
				for end < len(s) && s[end] >= '0' && s[end] <= '9' {
					end++
				}
				volume = end-j >= 1 && end-j <= 3 && (end == len(s) || strings.ContainsRune(" ._([)", rune(s[end])))
			}
			if !decimal && !ordinal && !abbreviation && !volume {
				out.WriteByte(' ')
				continue
			}
		} else if s[i] == '_' {
			out.WriteByte(' ')
			continue
		}
		out.WriteByte(s[i])
	}
	return out.String()
}
