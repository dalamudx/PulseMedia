package seerr

import (
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"strings"
	"testing"
)

func TestParseApprovedOnlyAndExplicitTVSeasons(t *testing.T) {
	for _, tc := range []struct {
		raw string
		ok  bool
	}{
		{`{"notification_type":"MEDIA_APPROVED","media":{"media_type":"movie","tmdbId":"550"},"request":{"request_id":"72"}}`, true},
		{`{"notification_type":"MEDIA_APPROVED","media":{"media_type":"tv","tmdbId":1399},"request":{"request_id":72},"extra":[{"name":"Requested Seasons","value":"2, 1"}]}`, true},
		{`{"notification_type":"MEDIA_AUTO_APPROVED","media":{"media_type":"movie","tmdbId":550},"request":{"request_id":72}}`, true},
		{`{"notification_type":"MEDIA_AUTO_APPROVED","media":{"media_type":"tv","tmdbId":1399},"request":{"request_id":72},"extra":[{"name":"Requested Seasons","value":"2, 1"}]}`, true},
		{`{"notification_type":"MEDIA_AUTO_APPROVED","media":{"media_type":"tv","tmdbId":1399},"request":{"request_id":72}}`, false},
		{`{"notification_type":"MEDIA_AUTO_APPROVED","media":{"media_type":"tv","tmdbId":1399},"request":{"request_id":72},"extra":[{"name":"Requested Seasons","value":""}]}`, false},
		{`{"notification_type":"MEDIA_AUTO_APPROVED","media":{"media_type":"tv","tmdbId":1399},"request":{"request_id":72},"extra":[{"name":"Requested Seasons","value":"1, all"}]}`, false},
		{`{"notification_type":"MEDIA_AUTO_APPROVED","media":{"media_type":"tv","tmdbId":1399},"request":{"request_id":72},"extra":[{"name":"Requested Seasons","value":"1"},{"name":"Requested Seasons","value":"2"}]}`, false},
		{`{"notification_type":"MEDIA_REQUESTED","media":{"media_type":"movie","tmdbId":550},"request":{"request_id":72}}`, false},
		{`{"notification_type":"media_auto_approved","media":{"media_type":"movie","tmdbId":550},"request":{"request_id":72}}`, false},
		{`{"notification_type":"MEDIA_AVAILABLE","media":{"media_type":"movie","tmdbId":550},"request":{"request_id":72}}`, false},
		{`{"notification_type":"MEDIA_APPROVED","media":{"media_type":"tv","tmdbId":1399},"request":{"request_id":72}}`, false},
		{`{"notification_type":"MEDIA_APPROVED","media":{"media_type":"movie","tmdbId":550,"tmdbId":551},"request":{"request_id":72}}`, false},
		{`{"notification_type":"MEDIA_APPROVED","media":{"media_type":"movie","tmdbId":550},"request":{"request_id":0}}`, false},
	} {
		out, e := ParseEvent([]byte(tc.raw))
		if tc.ok && (e != nil || out.ExternalRequestID != "72" || len(out.PayloadHash) != 64) {
			t.Fatalf("valid notification: %+v %v", out, e)
		}
		if !tc.ok && !errors.Is(e, ports.ErrRejected) {
			t.Fatalf("invalid notification accepted: %+v", out)
		}
	}
	if CheckJSON([]byte(strings.Repeat("[", 20)+strings.Repeat("]", 20))) == nil {
		t.Fatal("unbounded nesting accepted")
	}
}
func TestPayloadHashIncludesRequestScopeButNotJSONWhitespace(t *testing.T) {
	a := `{"notification_type":"MEDIA_APPROVED","media":{"media_type":"movie","tmdbId":550},"request":{"request_id":72}}`
	b := `{ "request":{"request_id":72},"media":{"tmdbId":550,"media_type":"movie"},"notification_type":"MEDIA_APPROVED" }`
	x, e := ParseEvent([]byte(a))
	if e != nil {
		t.Fatal(e)
	}
	y, e := ParseEvent([]byte(b))
	if e != nil || x.PayloadHash != y.PayloadHash {
		t.Fatal("equivalent objects did not deduplicate", e)
	}
	z, e := ParseEvent([]byte(strings.Replace(a, "550", "551", 1)))
	if e != nil || z.PayloadHash == x.PayloadHash {
		t.Fatal("changed payload reused hash", e)
	}
	auto, e := ParseEvent([]byte(strings.Replace(a, "MEDIA_APPROVED", "MEDIA_AUTO_APPROVED", 1)))
	if e != nil || auto.ExternalRequestID != x.ExternalRequestID || auto.Intent.MediaType != x.Intent.MediaType || auto.Intent.TMDBID != x.Intent.TMDBID || auto.PayloadHash == x.PayloadHash {
		t.Fatal("notification changes must retain request identity but conflict with the original payload", e)
	}
}
