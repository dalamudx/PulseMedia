package download

import (
	"github.com/dalamudx/PulseMedia/internal/ports"
	"math"
	"testing"
)

func TestInventoryValidation(t *testing.T) {
	valid := []ports.TorrentFile{{Index: 0, Name: "Fixture.S01E01.mkv", SizeBytes: 32, Progress: 1}}
	if validateInventory(valid, 32) != nil {
		t.Fatal("valid full inventory rejected")
	}
	for _, bad := range [][]ports.TorrentFile{nil, {{Index: 0, Name: "../escape.mkv", SizeBytes: 32, Progress: 1}}, {{Index: 0, Name: "a.mkv", SizeBytes: 32, Progress: 0.5}}, {{Index: 0, Name: "a.mkv", SizeBytes: 32, Progress: 1}, {Index: 0, Name: "b.mkv", SizeBytes: 0, Progress: 1}}, {{Index: 0, Name: "token=fixture.mkv", SizeBytes: 32, Progress: 1}}, {{Index: 0, Name: "a.mkv", SizeBytes: math.MaxInt64, Progress: 1}, {Index: 1, Name: "b.mkv", SizeBytes: 1, Progress: 1}}} {
		if validateInventory(bad, 32) == nil {
			t.Fatal("bad inventory accepted", bad)
		}
	}
	if validateInventory(valid, 33) == nil {
		t.Fatal("incomplete sum accepted")
	}
}
