package httpapi

import (
	"net/http"
	"net/url"
	"strconv"
	"unicode"
	"unicode/utf8"
)

const bangumiCalendarPath = "/api/v1/metadata/bangumi/calendar"

func bangumiCalendarNoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == bangumiCalendarPath {
			w.Header().Set("Cache-Control", "private, no-store")
		}
		next.ServeHTTP(w, r)
	})
}
func strictBangumiCalendarQuery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == bangumiCalendarPath && r.Method == http.MethodGet {
			if actor := GetActor(r.Context()); actor == nil || !metadataReadAllowed(actor) {
				next.ServeHTTP(w, r)
				return
			}
			query, err := url.ParseQuery(r.URL.RawQuery)
			valid := err == nil
			for key, values := range query {
				if len(values) != 1 {
					valid = false
					continue
				}
				switch key {
				case "weekday":
					n, e := strconv.Atoi(values[0])
					if e != nil || n < 1 || n > 7 || strconv.Itoa(n) != values[0] {
						valid = false
					}
				case "q":
					if !utf8.ValidString(values[0]) || utf8.RuneCountInString(values[0]) > 120 {
						valid = false
					}
					for _, char := range values[0] {
						if unicode.IsControl(char) {
							valid = false
						}
					}
				default:
					valid = false
				}
			}
			if !valid {
				WriteJSONError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid calendar filter", GetRequestID(r.Context()))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
