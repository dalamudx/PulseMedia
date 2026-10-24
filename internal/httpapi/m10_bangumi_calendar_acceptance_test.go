package httpapi_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/google/uuid"
)

const m10BangumiPath = "/api/v1/metadata/bangumi/calendar"

func (p *m5CreationProtocol) serveBangumi(t *testing.T, w http.ResponseWriter, r *http.Request) bool {
	t.Helper()
	if r.URL.Path != "/calendar" {
		return false
	}
	if r.Method != http.MethodGet || r.URL.RawQuery != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Accept") != "application/json" {
		t.Error("native Bangumi request carried a filter or credential")
	}
	p.mu.Lock()
	p.calls[r.URL.Path]++
	body, ok := p.bodies[r.URL.Path]
	status := p.calendarStatus
	p.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusServiceUnavailable)
		return true
	}
	if status == 0 {
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", "60")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
	return true
}
func m10BangumiWeek(t *testing.T) string {
	t.Helper()
	days := []any{}
	for day := 1; day <= 7; day++ {
		items := []any{}
		if day == 1 {
			items = append(items, map[string]any{"id": 12345, "type": 2, "name": "葬送のフリーレン", "name_cn": "葬送的芙莉莲", "air_date": "2023-09-29", "images": map[string]any{"large": "https://lain.bgm.tv/pic/cover/l/owned.jpg"}, "rating": map[string]any{"score": 8.9}})
		}
		days = append(days, map[string]any{"weekday": map[string]int{"id": day}, "items": items})
	}
	data, err := json.Marshal(days)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
func m10BangumiResponse(t *testing.T, r m5IndexerHTTPResult, status int) httpapi.BangumiCalendarResponse {
	t.Helper()
	m5IndexerStatus(t, r, status)
	if r.header.Get("Cache-Control") != "private, no-store" {
		t.Fatal("calendar response cacheable", status)
	}
	var out httpapi.BangumiCalendarResponse
	if status == http.StatusOK {
		if json.Unmarshal(r.body, &out) != nil || out.Days == nil || bool(out.ExecutionAllowed) || out.ObservedAt.IsZero() {
			t.Fatal("calendar response contract")
		}
	}
	return out
}
func TestM10BangumiCalendarCookieHTTPPG(t *testing.T) {
	f, p := newM5CreationAcceptance(t)
	p.set("/calendar", m10BangumiWeek(t))
	admin := m5CreationLogin(t, f, "creation-admin")
	m5InteractiveSQL(t, f, `INSERT INTO app_user(id,username,password_hash,role,is_active) SELECT $1,'calendar-member',password_hash,'member',true FROM app_user WHERE username='creation-admin'`, uuid.New())
	member := m5CreationLogin(t, f, "calendar-member")
	before := m9WordHTTPStable(t, f)
	nativeBefore := f.calls.Load()
	metaBefore := p.count()
	call := func(path, cookie string, status int) httpapi.BangumiCalendarResponse {
		t.Helper()
		return m10BangumiResponse(t, f.request(t, http.MethodGet, path, "", nil, cookie), status)
	}
	all := call(m10BangumiPath, admin, http.StatusOK)
	if len(all.Days) != 7 || len(all.Days[0].Items) != 1 {
		t.Fatal("week missing")
	}
	item := all.Days[0].Items[0]
	if item.BangumiId != "12345" || item.OriginalTitle != "葬送のフリーレン" || item.Title != "葬送的芙莉莲" || item.FirstAirDate == nil || *item.FirstAirDate != "2023-09-29" || item.Rating == nil || *item.Rating != 8.9 {
		t.Fatal("identity or native date lost")
	}
	for _, q := range []string{"芙莉莲", "フリーレン"} {
		out := call(m10BangumiPath+"?weekday=1&q="+url.QueryEscape(q), member, http.StatusOK)
		if len(out.Days) != 1 || len(out.Days[0].Items) != 1 || !out.ObservedAt.Equal(all.ObservedAt) {
			t.Fatal("bilingual cached result")
		}
	}
	empty := call(m10BangumiPath+"?weekday=7&q="+url.QueryEscape("芙莉莲"), member, http.StatusOK)
	if len(empty.Days) != 1 || empty.Days[0].Items == nil || len(empty.Days[0].Items) != 0 {
		t.Fatal("no-match became failure or invented data")
	}
	for _, q := range []string{"weekday=0", "weekday=8", "weekday=01", "weekday=1&weekday=2", "weekday=x", "q=a&q=b", "other=1", "q=%0A", "q=%FF", "%zz=1"} {
		call(m10BangumiPath+"?"+q, admin, http.StatusBadRequest)
	}
	call(m10BangumiPath, "", http.StatusUnauthorized)
	after := m9WordHTTPState(t, f)
	if !reflect.DeepEqual(before, after) || f.calls.Load() != nativeBefore || p.count() != metaBefore+1 || p.pathCount("/calendar") != 1 {
		t.Fatal("calendar/filter GET changed protected tables or made extra native calls")
	}
	m5InteractiveSQL(t, f, `UPDATE app_user SET is_active=false WHERE username='calendar-member'`)
	call(m10BangumiPath, member, http.StatusUnauthorized)
	if !reflect.DeepEqual(after, m9WordHTTPState(t, f)) || p.pathCount("/calendar") != 1 {
		t.Fatal("revocation/cache GET changed business state")
	}
	t.Log("M10_BANGUMI_PUBLIC_COOKIE_PASS Cookie=REAL App=REAL PG=18.6 weekdays=7 names=CHINESE_AND_JAPANESE source_reads=1 cache=SHARED filters=STRICT revoked_cache=DENIED protected=FULL_NON_AUTH download_calls=0 execution_allowed=false")
}
func TestM10BangumiCalendarErrorsAndEmptyHTTPPG(t *testing.T) {
	for _, tc := range []struct {
		name, body     string
		native, status int
	}{{"empty", "[]", http.StatusOK, http.StatusOK}, {"malformed", "null", http.StatusOK, http.StatusServiceUnavailable}, {"rate-limited", "owned-private-upstream", http.StatusTooManyRequests, http.StatusServiceUnavailable}, {"unavailable", "owned-private-upstream", http.StatusServiceUnavailable, http.StatusServiceUnavailable}} {
		t.Run(tc.name, func(t *testing.T) {
			f, p := newM5CreationAcceptance(t)
			p.set("/calendar", tc.body)
			p.mu.Lock()
			p.calendarStatus = tc.native
			p.mu.Unlock()
			before := m9WordHTTPStable(t, f)
			calls := f.calls.Load()
			r := f.request(t, http.MethodGet, m10BangumiPath, "", nil, f.session)
			out := m10BangumiResponse(t, r, tc.status)
			if tc.status == http.StatusOK && len(out.Days) != 0 {
				t.Fatal("empty upstream manufactured results")
			}
			if !reflect.DeepEqual(before, m9WordHTTPState(t, f)) || f.calls.Load() != calls || p.pathCount("/calendar") != 1 {
				t.Fatal("failed/empty read wrote business facts or retried")
			}
			if tc.status != http.StatusOK && string(r.body) == tc.body {
				t.Fatal("upstream detail leaked")
			}
			t.Log("M10_BANGUMI_FAILURE_COOKIE_PASS case=" + tc.name + " status=" + strconv.Itoa(tc.status) + " protected=FULL_NON_AUTH retries=0")
		})
	}
}
