package mediasidecar

import (
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestStatFingerprintParsing(t *testing.T) {
	for _, raw := range []string{"", "stat.v1:0:4096:123", "stat.v1:-1:4096:123", "stat.v1:+1:4096:123", "stat.v1:01:4096:123", "stat.v1:1:4097:123", "stat.v1:1:4096:124", "stat.v1:1:4096:123:extra", "stat.v2:1:4096:123", "stat.v1:9223372036854775808:4096:123"} {
		t.Run(raw, func(t *testing.T) {
			_, ok := sourceIdentity(db.LibraryFile{SizeBytes: 4096, MtimeNs: 123, FileFingerprint: pgtype.Text{String: raw, Valid: true}}, 42)
			if ok {
				t.Fatal("invalid stat fingerprint accepted")
			}
		})
	}
	got, ok := sourceIdentity(db.LibraryFile{SizeBytes: 4096, MtimeNs: 123, FileFingerprint: pgtype.Text{String: "stat.v1:1:4096:123", Valid: true}}, 42)
	if !ok || got != (ports.FileIdentity{Device: 42, Inode: 1, Size: 4096, Mtime: 123}) {
		t.Fatal(got, ok)
	}
}
func TestOpaquePosterAndRelativePaths(t *testing.T) {
	for _, value := range []string{"https://image.tmdb.org/a.jpg", "//evil.jpg", "/../a.jpg", "/a/b.jpg", "/%61.jpg", "/a.jpg?token=x", "/a.jpg#x", "/a\\b.jpg", "/a.svg", " /a.jpg"} {
		if posterPattern.MatchString(value) {
			t.Fatalf("unsafe provider path %q", value)
		}
	}
	for _, value := range []string{"../source.mkv", "Show/../source.mkv", "/source.mkv", "Show//source.mkv", "./source.mkv", "source\\file.mkv", "source\x00.mkv", "."} {
		if safeRelative(value) {
			t.Fatalf("unsafe relative path %q", value)
		}
	}
}
func TestPayloadRejectsAlienShape(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `[]`, `{"version":1,"source_event_id":"invalid"}`, `{"version":1} {"extra":true}`} {
		if _, err := parsePayload([]byte(raw)); err != ports.ErrMediaSidecarStale {
			t.Fatalf("invalid payload accepted: %v", err)
		}
	}
}
