package torrent_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func tvUpgradeProofTarget() media.TVEpisodeUpgradeTarget {
	return media.TVEpisodeUpgradeTarget{Unit: media.NumberingUnit{ID: uuid.MustParse("7e7c440e-5fd4-4f7b-955f-ad6687bd1261"), EpisodeID: 1003, Season: 1, Episode: 3, Key: "S01E03", Version: 7}, Title: "Fixture", Year: 2020, TMDBID: 100}
}

func TestTVEpisodeUpgradeMetainfoNativeProof(t *testing.T) {
	name := "Fixture.2020.S01E03.mkv"
	raw := []byte("d4:infod6:lengthi7e4:name23:" + name + "12:piece lengthi16384e6:pieces20:01234567890123456789ee")
	original := bytes.Clone(raw)
	p, err := ports.InspectTVEpisodeUpgradeMetainfo(torrent.Inspector{}, raw, tvUpgradeProofTarget())
	if err != nil || p == nil || !p.ValidateBinding() {
		t.Fatal("native proof rejected", err)
	}
	if p.Target() != tvUpgradeProofTarget() || p.Video().Path != name || p.Video().Size != 7 || p.TotalBytes() != 7 || len(p.InfoHash()) != 40 || p.SHA256() == [32]byte{} {
		t.Fatal("wrong native binding")
	}
	raw[0] = '!'
	p.Bytes()[0] = '!'
	p.Files()[0].Path = "changed"
	video, target := p.Video(), p.Target()
	video.Path, target.Title = "changed", "Other"
	if !p.ValidateBinding() || !bytes.Equal(p.Bytes(), original) || p.Video().Path != name || p.Target() != tvUpgradeProofTarget() {
		t.Fatal("mutable proof")
	}
	wrong := tvUpgradeProofTarget()
	wrong.Unit.Key, wrong.Unit.Episode = "S01E04", 4
	if q, e := ports.InspectTVEpisodeUpgradeMetainfo(torrent.Inspector{}, original, wrong); q != nil || e != ports.ErrTVEpisodeUpgradeMetainfoReview {
		t.Fatal("wrong target accepted")
	}
	if q, e := ports.InspectTVEpisodeUpgradeMetainfo(torrent.Inspector{}, []byte("secret invalid metainfo"), tvUpgradeProofTarget()); q != nil || e != ports.ErrTVEpisodeUpgradeMetainfoReview {
		t.Fatal("invalid native bytes accepted")
	}
}

//nolint:staticcheck // Verify JSON cannot construct private proof evidence.
func TestTVEpisodeUpgradeMetainfoZeroAndJSON(t *testing.T) {
	for _, p := range []*ports.TVEpisodeUpgradeMetainfoProof{nil, {}} {
		if p.ValidateBinding() || len(p.Bytes()) != 0 || len(p.Files()) != 0 || p.TotalBytes() != 0 || p.InfoHash() != "" || p.SHA256() != [32]byte{} || p.Target() != (media.TVEpisodeUpgradeTarget{}) || p.Video() != (media.TVTorrentUnitFile{}) {
			t.Fatal("zero proof evidence")
		}
	}
	var p ports.TVEpisodeUpgradeMetainfoProof
	if err := json.Unmarshal([]byte(`{"metainfo":{},"target":{},"video":{}}`), &p); err != nil || p.ValidateBinding() {
		t.Fatal("JSON minted proof", err)
	}
	if q, e := ports.InspectTVEpisodeUpgradeMetainfo(nil, []byte("secret"), tvUpgradeProofTarget()); q != nil || e != ports.ErrTVEpisodeUpgradeMetainfoReview {
		t.Fatal("nil inspector accepted")
	}
}
