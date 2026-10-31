package library

import (
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"testing"
)

func TestCompanionMatchingUniqueStemLanguageAndMovedVideo(t *testing.T) {
	m := ports.UntrackedOrganizationMaterial{Operation: ports.UntrackedOrganizationOperation{ID: uuid.New(), MaterialHash: "frozen"}}
	m.Confirmation.Material.File.RelativePath = "dir/Movie.2026.mkv"
	m.Confirmation.Material.Input.TargetRelativePath = "Movie (2026)/Movie.mkv"
	id := ports.FileIdentity{Device: 1, Inode: 2, Size: 12, Mtime: 3}
	m.Confirmation.Material.File.Identity = id
	entries := []ports.UntrackedFileObservation{}
	for _, name := range []string{"dir/Movie.2026.mkv", "dir/Movie.2026.zh-Hans.srt", "dir/Movie.2026.en.forced.ass", "dir/Movie.2026.srt", "dir/Movie.2026.default.srt", "other/Movie.2026.chs.srt", "dir/Movie.2026.extra.srt", "dir/Other.srt"} {
		entries = append(entries, ports.UntrackedFileObservation{RelativePath: name, Identity: id})
	}
	got := matchCompanions(m, entries)
	if len(got) != 3 {
		t.Fatalf("matching candidates: %+v", got)
	}
	for _, c := range got {
		if len(c.Key) != 64 || c.Identity != id {
			t.Fatal("identity not frozen")
		}
	}
	if got[0].TargetRelativePath != "Movie (2026)/Movie.en.forced.ass" || got[2].TargetRelativePath != "Movie (2026)/Movie.zh-Hans.srt" {
		t.Fatalf("playable suffix not retained: %+v", got)
	}
	moved := matchCompanions(m, entries[1:])
	if len(moved) != 3 {
		t.Fatal("moved original lost frozen stem")
	}
	duplicate := append(entries, ports.UntrackedFileObservation{RelativePath: "dir/Movie.2026.mp4", Identity: id})
	if len(matchCompanions(m, duplicate)) != 0 {
		t.Fatal("ambiguous same-stem videos assigned companions")
	}
	duplicate = append(entries[1:], ports.UntrackedFileObservation{RelativePath: "dir/Movie.2026.mp4", Identity: id})
	if len(matchCompanions(m, duplicate)) != 0 {
		t.Fatal("moved original reassigned to another video")
	}
	changed := append([]ports.UntrackedFileObservation{}, entries...)
	changed[0].Identity.Inode++
	if len(matchCompanions(m, changed)) != 0 {
		t.Fatal("replacement video reused original subtitle matching authority")
	}
	changed[0].Identity = id
	changed[1].Identity.Inode++
	if matchCompanions(m, changed)[2].Key == got[2].Key {
		t.Fatal("file replacement reused observation key")
	}
}
func TestCompanionMatchingRejectsTargetCollisionsAndEmpty(t *testing.T) {
	m := ports.UntrackedOrganizationMaterial{Operation: ports.UntrackedOrganizationOperation{ID: uuid.New()}}
	m.Confirmation.Material.File.RelativePath = "Movie.mkv"
	m.Confirmation.Material.Input.TargetRelativePath = "target/Movie.mkv"
	id := ports.FileIdentity{Device: 1, Inode: 2, Size: 3, Mtime: 4}
	entries := []ports.UntrackedFileObservation{{RelativePath: "Movie.en.srt", Identity: id}, {RelativePath: "Movie.EN.SRT", Identity: id}, {RelativePath: "Movie.ass"}}
	if got := matchCompanions(m, entries); len(got) != 0 {
		t.Fatalf("case-colliding target/empty subtitle accepted %+v", got)
	}
	if got := matchCompanions(m, nil); len(got) != 0 {
		t.Fatal("empty companions manufactured a candidate")
	}
}
