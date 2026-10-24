package bangumi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

func decodeCalendar(data []byte) ([]DaySchedule, error) {
	if !uniqueJSON(data) {
		return nil, ports.ErrBadResponse
	}
	var raw []json.RawMessage
	if json.Unmarshal(data, &raw) != nil || raw == nil || (len(raw) != 0 && len(raw) != 7) {
		return nil, ports.ErrBadResponse
	}
	days := make([]DaySchedule, 0, len(raw))
	seen := map[int]bool{}
	total := 0
	for _, record := range raw {
		var fields map[string]json.RawMessage
		var weekday map[string]json.RawMessage
		var id int
		var rows []json.RawMessage
		if json.Unmarshal(record, &fields) != nil || json.Unmarshal(fields["weekday"], &weekday) != nil || json.Unmarshal(weekday["id"], &id) != nil || id < 1 || id > 7 || seen[id] || json.Unmarshal(fields["items"], &rows) != nil || rows == nil || len(rows) > 500 {
			return nil, ports.ErrBadResponse
		}
		seen[id] = true
		total += len(rows)
		if total > 2000 {
			return nil, ports.ErrBadResponse
		}
		day := DaySchedule{DayID: id, DayName: []string{"星期一", "星期二", "星期三", "星期四", "星期五", "星期六", "星期日"}[id-1], Items: make([]AnimeItem, 0, len(rows))}
		ids := map[int64]bool{}
		for _, row := range rows {
			item, err := decodeItem(row)
			if err != nil || ids[item.ID] {
				return nil, ports.ErrBadResponse
			}
			ids[item.ID] = true
			day.Items = append(day.Items, item)
		}
		days = append(days, day)
	}
	sort.Slice(days, func(i, j int) bool { return days[i].DayID < days[j].DayID })
	return days, nil
}
func decodeItem(raw json.RawMessage) (AnimeItem, error) {
	var f map[string]json.RawMessage
	var id int64
	var kind int
	if json.Unmarshal(raw, &f) != nil || json.Unmarshal(f["id"], &id) != nil || id <= 0 || json.Unmarshal(f["type"], &kind) != nil || kind != 2 {
		return AnimeItem{}, ports.ErrBadResponse
	}
	original, ok := text(f, "name")
	if !ok {
		return AnimeItem{}, ports.ErrBadResponse
	}
	chinese, ok := text(f, "name_cn")
	if !ok || original == "" && chinese == "" {
		return AnimeItem{}, ports.ErrBadResponse
	}
	date, ok := text(f, "air_date")
	if !ok {
		return AnimeItem{}, ports.ErrBadResponse
	}
	if date == "0000-00-00" {
		date = ""
	}
	if date != "" {
		d, err := time.Parse("2006-01-02", date)
		if err != nil || d.Year() < 1 || d.Format("2006-01-02") != date {
			return AnimeItem{}, ports.ErrBadResponse
		}
	}
	title := chinese
	if title == "" {
		title = original
	}
	item := AnimeItem{ID: id, Title: title, OrigTitle: original, AirDate: date}
	if value := f["images"]; len(value) > 0 && !bytes.Equal(value, []byte("null")) {
		var images map[string]json.RawMessage
		if json.Unmarshal(value, &images) != nil || images == nil {
			return AnimeItem{}, ports.ErrBadResponse
		}
		for _, key := range []string{"large", "common", "medium", "small", "grid"} {
			candidate, ok := text(images, key)
			if !ok {
				return AnimeItem{}, ports.ErrBadResponse
			}
			if safe := poster(candidate); safe != "" {
				item.PosterURL = safe
				break
			}
		}
	}
	if value := f["rating"]; len(value) > 0 && !bytes.Equal(value, []byte("null")) {
		var rating map[string]json.RawMessage
		if json.Unmarshal(value, &rating) != nil || rating == nil {
			return AnimeItem{}, ports.ErrBadResponse
		}
		if score := rating["score"]; len(score) > 0 && !bytes.Equal(score, []byte("null")) {
			var n float64
			if json.Unmarshal(score, &n) != nil || n < 0 || n > 10 {
				return AnimeItem{}, ports.ErrBadResponse
			}
			if n > 0 {
				item.Rating = &n
			}
		}
	}
	return item, nil
}
func text(fields map[string]json.RawMessage, key string) (string, bool) {
	raw := fields[key]
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", true
	}
	var s string
	if json.Unmarshal(raw, &s) != nil || !utf8.ValidString(s) || utf8.RuneCountInString(s) > 2048 {
		return "", false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", false
		}
	}
	return strings.TrimSpace(s), true
}
func poster(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host != "lain.bgm.tv" || u.RawQuery != "" || u.Fragment != "" || !strings.HasPrefix(u.Path, "/pic/cover/") {
		return ""
	}
	u.Scheme = "https"
	return u.String()
}
func uniqueJSON(data []byte) bool {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var value func(int) bool
	value = func(depth int) bool {
		if depth > 48 {
			return false
		}
		token, err := d.Token()
		if err != nil {
			return false
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return true
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return false
				}
				name, ok := key.(string)
				name = strings.ToLower(name)
				if !ok || seen[name] {
					return false
				}
				seen[name] = true
				if !value(depth + 1) {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim('}')
		case '[':
			for d.More() {
				if !value(depth + 1) {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim(']')
		default:
			return false
		}
	}
	if !value(0) {
		return false
	}
	_, err := d.Token()
	return errors.Is(err, io.EOF)
}
