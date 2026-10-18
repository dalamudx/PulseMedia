package torrent

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// Literal bencode, with hashes independently frozen using sha1sum/sha256sum.
const movieUpgradeInfo = "d6:lengthi4e4:name19:Fight.Club.1999.mkv12:piece lengthi4e6:pieces20:00000000000000000000e"
const movieUpgradeRaw = "d4:info" + movieUpgradeInfo + "e"

func proofMovieTarget() media.MovieUpgradeTarget {
	return media.MovieUpgradeTarget{MediaID: uuid.MustParse("11111111-1111-4111-8111-111111111111"), UnitID: uuid.MustParse("22222222-2222-4222-8222-222222222222"), MediaVersion: 3, UnitVersion: 4, MediaType: media.TypeMovie, UnitKey: "movie", OriginalTitle: "Fight Club", Year: 1999, Identities: []media.Identity{{Namespace: "tmdb.movie", ExternalID: "550"}}, Aliases: []string{"搏击俱乐部"}, ParserVersion: media.EpisodeParserVersion}
}
func movieBS(s string) string { return strconv.Itoa(len(s)) + ":" + s }
func movieFile(name string, size int) string {
	return "d6:lengthi" + strconv.Itoa(size) + "e4:pathl" + movieBS(name) + "ee"
}
func movieMulti(parent string, files ...string) []byte {
	return []byte("d4:infod5:filesl" + strings.Join(files, "") + "e4:name" + movieBS(parent) + "12:piece lengthi4e6:pieces20:00000000000000000000ee")
}
func TestMovieUpgradeProofExactRawAndCopies(t *testing.T) {
	input := []byte(movieUpgradeRaw)
	target := proofMovieTarget()
	p, err := InspectMovieUpgradeMetainfo(input, target)
	if err != nil || !p.ValidateBinding() {
		t.Fatal(err)
	}
	expectedDigest, _ := hex.DecodeString("7f0c22104faff6d1657da87c4f1060f1fc0475eb0da127322c85e42eda56a7e8")
	digest := p.SHA256()
	if p.InfoHash() != "c9143a21559ade8bd53fed0f7792dd78d47fe727" || !bytes.Equal(digest[:], expectedDigest) || p.TotalBytes() != 4 || len(p.Files()) != 1 || p.Video().Path != "Fight.Club.1999.mkv" || p.Video().MediaID != target.MediaID || p.Video().UnitID != target.UnitID {
		t.Fatal("literal evidence mismatch")
	}
	input[0] = 'x'
	target.Aliases[0] = "Other"
	target.Identities[0].ExternalID = "551"
	p.Bytes()[0] = 'x'
	p.Files()[0].Path = "other.mkv"
	view := p.Target()
	view.Aliases[0] = "Other"
	view.Identities[0].ExternalID = "551"
	video := p.Video()
	video.Path = "other.mkv"
	if !p.ValidateBinding() || string(p.Bytes()) != movieUpgradeRaw || p.Target().Aliases[0] != "搏击俱乐部" || p.Target().Identities[0].ExternalID != "550" || p.Files()[0].Path != "Fight.Club.1999.mkv" {
		t.Fatal("evidence retained caller-owned storage")
	}
	other, err := InspectMovieUpgradeMetainfo([]byte("d7:comment1:x4:info"+movieUpgradeInfo+"e"), proofMovieTarget())
	if err != nil || other.InfoHash() != p.InfoHash() || other.SHA256() == p.SHA256() {
		t.Fatal("envelope and info identity conflated", err)
	}
	// Existing TV authority has a separate >=2-file requirement; no relaxation.
	if tv, err := ports.InspectCompleteTVMetainfo(Inspector{}, []byte(movieUpgradeRaw)); tv != nil || !errors.Is(err, ports.ErrCompleteTVSubmissionReview) {
		t.Fatal("single movie minted TV proof")
	}
}
func TestMovieUpgradeProofUnknownForgeryAndMutation(t *testing.T) {
	var empty MovieUpgradeMetainfoProof
	var nilProof *MovieUpgradeMetainfoProof
	for _, p := range []*MovieUpgradeMetainfoProof{nilProof, &empty} {
		if p.ValidateBinding() || p.Bytes() != nil || p.Files() != nil || p.InfoHash() != "" || p.TotalBytes() != 0 || p.SHA256() != ([32]byte{}) {
			t.Fatal("unknown proof accepted")
		}
	}
	// Public DTO claims and even a serialized valid proof cannot mint evidence.
	for _, raw := range []string{`{"raw":"ZA==","issued":true,"hash":"c9143a21559ade8bd53fed0f7792dd78d47fe727","Files":[{"Path":"Fight.Club.1999.mkv","Size":4}]}`, `{}`} {
		var fake MovieUpgradeMetainfoProof
		//nolint:staticcheck // SA9005 is intentional: public JSON must not mint private evidence.
		if err := json.Unmarshal([]byte(raw), &fake); err != nil {
			t.Fatal(err)
		}
		if fake.ValidateBinding() {
			t.Fatal("JSON forgery accepted")
		}
	}
	for name, mutate := range map[string]func(*MovieUpgradeMetainfoProof){
		"raw":       func(p *MovieUpgradeMetainfoProof) { p.raw[0] = 'x' },
		"digest":    func(p *MovieUpgradeMetainfoProof) { p.digest[0] ^= 1 },
		"info_hash": func(p *MovieUpgradeMetainfoProof) { p.hash = strings.Repeat("0", 40) },
		"total":     func(p *MovieUpgradeMetainfoProof) { p.total++ },
		"files":     func(p *MovieUpgradeMetainfoProof) { p.files[0].Size++ },
		"target":    func(p *MovieUpgradeMetainfoProof) { p.target.UnitVersion++ },
		"alias":     func(p *MovieUpgradeMetainfoProof) { p.target.Aliases[0] = "Other" },
		"identity":  func(p *MovieUpgradeMetainfoProof) { p.target.Identities[0].ExternalID = "551" },
		"video":     func(p *MovieUpgradeMetainfoProof) { p.video.UnitID = uuid.Nil },
		"seal":      func(p *MovieUpgradeMetainfoProof) { p.seal[0] ^= 1 },
		"issuance":  func(p *MovieUpgradeMetainfoProof) { p.issued = false },
	} {
		t.Run(name, func(t *testing.T) {
			p, err := InspectMovieUpgradeMetainfo([]byte(movieUpgradeRaw), proofMovieTarget())
			if err != nil {
				t.Fatal(err)
			}
			mutate(p)
			if p.ValidateBinding() || p.Bytes() != nil || p.Files() != nil || p.Video() != (media.MovieUpgradeUnitFile{}) {
				t.Fatal("modified evidence accepted")
			}
		})
	}
}
func TestMovieUpgradeProofRawReview(t *testing.T) {
	for name, raw := range map[string][]byte{
		"empty":           nil,
		"magnet":          []byte("magnet:?xt=urn:btih:private"),
		"trailing":        []byte(movieUpgradeRaw + "e"),
		"duplicate_info":  []byte("d4:info" + movieUpgradeInfo + "4:info" + movieUpgradeInfo + "e"),
		"bad_integer":     []byte(strings.Replace(movieUpgradeRaw, "i4e", "i04e", 1)),
		"hybrid":          []byte(strings.Replace(movieUpgradeRaw, "4:name", "12:meta versioni2e4:name", 1)),
		"symlink":         []byte("d4:infod4:attr1:l" + movieUpgradeInfo[1:] + "e"),
		"multi_symlink":   movieMulti("Fight Club", "d4:attr1:l6:lengthi4e4:pathl19:Fight.Club.1999.mkvee"),
		"split_rar":       movieMulti("Fight Club", movieFile("Fight.Club.1999.mkv", 4), movieFile("Fight.Club.1999.part01.rar", 0)),
		"zero_unknown":    movieMulti("Fight Club", movieFile("Fight.Club.1999.mkv", 4), movieFile("install.exe", 0)),
		"parent_conflict": movieMulti("Inception.2010", movieFile("Fight.Club.1999.mkv", 4)),
		"parent_tv":       movieMulti("Show.S01E01-E03", movieFile("Fight.Club.1999.mkv", 4)),
		"second_video":    movieMulti("Fight Club", movieFile("Fight.Club.1999.mkv", 2), movieFile("Inception.2010.mkv", 2)),
		"escape":          movieMulti("Fight Club", "d6:lengthi4e4:pathl2:..19:Fight.Club.1999.mkvee"),
		"absolute":        []byte(strings.Replace(movieUpgradeRaw, "19:Fight.Club.1999.mkv", "20:/Fight.Club.1999.mkv", 1)),
		"duplicate":       movieMulti("Fight Club", movieFile("Fight.Club.1999.mkv", 4), movieFile("Fight.Club.1999.mkv", 0)),
	} {
		t.Run(name, func(t *testing.T) {
			p, err := InspectMovieUpgradeMetainfo(raw, proofMovieTarget())
			if p != nil || !errors.Is(err, ErrMovieUpgradeMetainfoReview) {
				t.Fatal("unsafe raw evidence accepted", err)
			}
		})
	}
}
func TestMovieUpgradeProofZeroSidecarAndSizeBound(t *testing.T) {
	raw := movieMulti("Fight Club", movieFile("Fight.Club.1999.mkv", 4), movieFile("readme.txt", 0))
	p, err := InspectMovieUpgradeMetainfo(raw, proofMovieTarget())
	if err != nil || len(p.Files()) != 2 || p.Files()[1].Size != 0 || p.TotalBytes() != 4 {
		t.Fatal("zero sidecar discarded", err)
	}
	// A valid envelope exactly at 10MiB is accepted, one byte larger is review.
	n := ports.MaxTorrentMetainfoBytes - len(movieUpgradeRaw) - len("7:comment") - len("10485760:")
	for {
		delta := ports.MaxTorrentMetainfoBytes - (len(movieUpgradeRaw) + len("7:comment") + len(movieBS(strings.Repeat("x", n))))
		if delta == 0 {
			break
		}
		n += delta
	}
	atLimit := []byte("d7:comment" + movieBS(strings.Repeat("x", n)) + "4:info" + movieUpgradeInfo + "e")
	if len(atLimit) != ports.MaxTorrentMetainfoBytes {
		t.Fatal("bad boundary fixture")
	}
	if p, err := InspectMovieUpgradeMetainfo(atLimit, proofMovieTarget()); err != nil || !p.ValidateBinding() {
		t.Fatal("10MiB rejected", err)
	}
	if p, err := InspectMovieUpgradeMetainfo(append(atLimit, 'e'), proofMovieTarget()); p != nil || !errors.Is(err, ErrMovieUpgradeMetainfoReview) {
		t.Fatal("oversize accepted")
	}
}
func TestMovieUpgradeProofConcurrentCopies(t *testing.T) {
	p, err := InspectMovieUpgradeMetainfo([]byte(movieUpgradeRaw), proofMovieTarget())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 10 {
				p.Bytes()[0] = 'x'
				p.Files()[0].Path = "other"
				v := p.Target()
				v.Aliases[0] = "other"
				if !p.ValidateBinding() {
					t.Error("copy mutation invalidated shared proof")
				}
			}
		})
	}
	wg.Wait()
}
