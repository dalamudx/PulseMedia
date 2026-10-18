package ports_test

import (
	"github.com/dalamudx/PulseMedia/internal/ports"
	"strings"
	"testing"
)

func TestDownloadMagnetAdmission(t *testing.T) {
	hash := strings.Repeat("a", 40)
	for _, raw := range []string{"magnet:?xt=urn:btih:" + hash, "magnet:?xt=urn:btih:" + strings.ToUpper(hash) + "&dn=Movie&tr=https%3A%2F%2Ftracker.invalid%2Fannounce", "magnet:?xt=urn:btih:VKVKVKVKVKVKVKVKVKVKVKVKVKVKVKVK"} {
		got, err := ports.DownloadMagnetHash(raw)
		if err != nil || got != hash {
			t.Fatalf("legitimate magnet: %s %v", got, err)
		}
	}
	v2 := strings.Repeat("b", 64)
	for _, tc := range []struct{ link, want string }{
		{"magnet:?xt=urn:btmh:1220" + v2, v2},
		{"magnet:?xt=urn:btih:" + hash + "&xt=urn:btmh:1220" + v2, hash},
		{"magnet:?xt=urn:btmh:1220" + v2 + "&xt=urn:btih:" + hash, hash},
	} {
		got, err := ports.DownloadMagnetHash(tc.link)
		if err != nil || got != tc.want {
			t.Fatal("v2/hybrid magnet", got, err)
		}
		if err = ports.ValidateDownloadPayload(ports.AddTorrentParam{DownloadURL: tc.link, InfoHash: tc.want}); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{
		"magnet:?xt=urn:btmh:1220bad", "magnet:?xt=urn:btmh:1220" + v2 + "&xt=urn:btmh:1220" + v2,
		"http://127.0.0.1/probe", "file:///private", "magnet:?xt=urn:btih:" + hash + "&xs=http://127.0.0.1/probe", "magnet:?xt=urn:btih:" + hash + "&as=http://127.0.0.1/probe", "magnet:?xt=urn:btih:" + hash + "&ws=http://127.0.0.1/probe", "magnet:?xt=urn:btih:" + hash + "&so=0", "magnet:?xt=urn:btih:" + hash + "&xt=urn:btih:" + hash, "magnet:?xt=urn:btih:bad", "magnet://host?xt=urn:btih:" + hash, "magnet:?xt=urn:btih:" + hash + "#fragment", "magnet:?xt=urn:btih:" + hash + "%0ahttp://127.0.0.1/probe",
	} {
		if _, err := ports.DownloadMagnetHash(raw); err == nil {
			t.Error("unsafe magnet admitted")
		}
	}
	if err := ports.ValidateDownloadPayload(ports.AddTorrentParam{DownloadURL: "magnet:?xt=urn:btih:" + hash, InfoHash: strings.Repeat("b", 40)}); err == nil {
		t.Fatal("identity mismatch admitted")
	}
}
