package torrent_test

import (
	"bytes"
	"errors"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func bs(s string) string        { return strconv.Itoa(len(s)) + ":" + s }
func bi(n int64) string         { return "i" + strconv.FormatInt(n, 10) + "e" }
func bl(parts ...string) string { return "l" + strings.Join(parts, "") + "e" }
func bd(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := "d"
	for _, k := range keys {
		out += bs(k) + values[k]
	}
	return out + "e"
}
func file(name string, length int64) string {
	return bd(map[string]string{"length": bi(length), "path": bl(bs(name))})
}
func validInfo() map[string]string {
	return map[string]string{"files": bl(file("Show.S01E01.mkv", 4), file("Show.S01E02.mkv", 4)), "name": bs("Show"), "piece length": bi(4), "pieces": bs(strings.Repeat("0", 40))}
}
func metainfo(info map[string]string) []byte { return []byte(bd(map[string]string{"info": bd(info)})) }

func TestV1MetainfoExactBytesAndInfoIdentity(t *testing.T) {
	raw := metainfo(validInfo())
	result, err := (torrent.Inspector{}).InspectTorrent(raw)
	if err != nil {
		t.Fatal(err)
	}
	// Independently frozen from the literal info bytes with sha1sum before probing.
	if result.InfoHash != "bcf668b709aba7088dfa2002d901fc15af202656" || result.TotalBytes != 8 || len(result.Files) != 2 || result.Files[0].Path != "Show/Show.S01E01.mkv" || result.Files[1].Size != 4 || !bytes.Equal(result.TorrentBytes, raw) {
		t.Fatalf("unexpected file evidence: %#v", result)
	}
	raw[0] = 'x'
	if result.TorrentBytes[0] != 'd' {
		t.Fatal("inspector retained caller-owned bytes")
	}
	changed := []byte(bd(map[string]string{"announce": bs("https://tracker.invalid/private-passkey"), "info": bd(validInfo())}))
	other, err := (torrent.Inspector{}).InspectTorrent(changed)
	if err != nil || other.InfoHash != result.InfoHash || other.BytesSHA256 == result.BytesSHA256 {
		t.Fatal("raw envelope changes must keep info identity but change byte seal", err)
	}
}

func TestV1MetainfoSingleFileAndZeroLengthSidecar(t *testing.T) {
	single := map[string]string{"length": bi(4), "name": bs("Show.S01E01.mkv"), "piece length": bi(4), "pieces": bs(strings.Repeat("0", 20))}
	got, err := (torrent.Inspector{}).InspectTorrent(metainfo(single))
	if err != nil || len(got.Files) != 1 || got.Files[0].Path != "Show.S01E01.mkv" {
		t.Fatal(got, err)
	}
	info := validInfo()
	info["files"] = bl(file("Show.S01E01.mkv", 4), file("Show.S01E02.mkv", 4), file("Show.nfo", 0))
	got, err = (torrent.Inspector{}).InspectTorrent(metainfo(info))
	if err != nil || len(got.Files) != 3 || got.TotalBytes != 8 {
		t.Fatal(got, err)
	}
}

func TestV1MetainfoRefusesMalformedAndAmbiguousEvidence(t *testing.T) {
	cases := map[string][]byte{
		"empty":                  nil,
		"magnet":                 []byte("magnet:?xt=urn:btih:private-passkey"),
		"trailing_bytes":         append(metainfo(validInfo()), 'e'),
		"duplicate_info_key":     []byte("d4:info" + bd(validInfo()) + "4:info" + bd(validInfo()) + "e"),
		"unsorted_keys":          []byte("d4:info" + bd(validInfo()) + "8:announce1:xe"),
		"noncanonical_integer":   []byte(strings.Replace(string(metainfo(validInfo())), "i4e", "i04e", 1)),
		"negative_zero":          []byte(strings.Replace(string(metainfo(validInfo())), "i4e", "i-0e", 1)),
		"integer_overflow":       []byte(strings.Replace(string(metainfo(validInfo())), "i4e", "i9223372036854775808e", 1)),
		"string_length_overflow": []byte("d4:info9223372036854775808:xe"),
		"depth_limit":            []byte("d4:info" + strings.Repeat("l", 40) + strings.Repeat("e", 40) + "e"),
		"node_limit":             []byte("d4:info" + "l" + strings.Repeat("i0e", 50001) + "ee"),
		"size_limit":             bytes.Repeat([]byte{'x'}, ports.MaxTorrentMetainfoBytes+1),
	}
	mutations := map[string]func(map[string]string){
		"missing_name":          func(i map[string]string) { delete(i, "name") },
		"missing_pieces":        func(i map[string]string) { delete(i, "pieces") },
		"wrong_piece_count":     func(i map[string]string) { i["pieces"] = bs(strings.Repeat("0", 20)) },
		"incomplete_piece_hash": func(i map[string]string) { i["pieces"] = bs("x") },
		"zero_piece_length":     func(i map[string]string) { i["piece length"] = bi(0) },
		"negative_file":         func(i map[string]string) { i["files"] = bl(file("Show.S01E01.mkv", -1)) },
		"total_overflow":        func(i map[string]string) { i["files"] = bl(file("A.mkv", 9223372036854775807), file("B.mkv", 1)) },
		"empty_files":           func(i map[string]string) { i["files"] = bl() },
		"both_single_and_multi": func(i map[string]string) { i["length"] = bi(8) },
		"v2_or_hybrid":          func(i map[string]string) { i["meta version"] = bi(2) },
		"v2_tree":               func(i map[string]string) { i["file tree"] = bd(map[string]string{}) },
		"absolute_name":         func(i map[string]string) { i["name"] = bs("/Show") },
		"traversal":             func(i map[string]string) { i["files"] = bl(file("..", 8)) },
		"embedded_separator":    func(i map[string]string) { i["files"] = bl(file("S01/Show.mkv", 8)) },
		"backslash":             func(i map[string]string) { i["files"] = bl(file("S01\\Show.mkv", 8)) },
		"drive_or_stream":       func(i map[string]string) { i["name"] = bs("C:Show") },
		"control_byte":          func(i map[string]string) { i["name"] = bs("Show\x00") },
		"non_utf8":              func(i map[string]string) { i["name"] = bs(string([]byte{255})) },
		"trailing_dot_alias":    func(i map[string]string) { i["name"] = bs("Show.") },
		"device_name_alias":     func(i map[string]string) { i["name"] = bs("CON.mkv") },
		"long_component":        func(i map[string]string) { i["name"] = bs(strings.Repeat("x", 256)) },
		"different_utf8_name":   func(i map[string]string) { i["name.utf-8"] = bs("Other") },
		"different_utf8_path": func(i map[string]string) {
			i["files"] = bl(bd(map[string]string{"length": bi(8), "path": bl(bs("Show.mkv")), "path.utf-8": bl(bs("Other.mkv"))}))
		},
		"symlink": func(i map[string]string) {
			i["files"] = bl(bd(map[string]string{"attr": bs("l"), "length": bi(8), "path": bl(bs("Show.mkv")), "symlink path": bl(bs("outside"))}))
		},
		"symlink_without_attr":    func(i map[string]string) { i["symlink path"] = bl(bs("outside")) },
		"padding_requires_review": func(i map[string]string) { i["attr"] = bs("p") },
		"duplicate_path":          func(i map[string]string) { i["files"] = bl(file("A.mkv", 4), file("A.mkv", 4)) },
		"case_alias":              func(i map[string]string) { i["files"] = bl(file("A.mkv", 4), file("a.mkv", 4)) },
		"file_directory_collision": func(i map[string]string) {
			i["files"] = bl(file("folder", 4), bd(map[string]string{"length": bi(4), "path": bl(bs("folder"), bs("A.mkv"))}))
		},
	}
	for name, change := range mutations {
		i := validInfo()
		change(i)
		cases[name] = metainfo(i)
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := (torrent.Inspector{}).InspectTorrent(raw)
			if got != nil || !errors.Is(err, ports.ErrInvalidTorrentMetainfo) || strings.Contains(err.Error(), "private-passkey") {
				t.Fatal("unsafe metainfo accepted or leaked", err)
			}
		})
	}
}

func TestV1MetainfoToExactCompleteTVCoverage(t *testing.T) {
	units := []media.NumberingUnit{
		{ID: uuid.MustParse("11111111-1111-4111-8111-111111111111"), EpisodeID: 101, Season: 1, Episode: 1, Key: "S01E01", Version: 3},
		{ID: uuid.MustParse("22222222-2222-4222-8222-222222222222"), EpisodeID: 102, Season: 1, Episode: 2, Key: "S01E02", Version: 4},
	}
	rows := []media.NumberingRow{
		{SourceNumber: 1, EpisodeID: 101, UnitID: units[0].ID, UnitKey: "S01E01", UnitVersion: 3},
		{SourceNumber: 2, EpisodeID: 102, UnitID: units[1].ID, UnitKey: "S01E02", UnitVersion: 4},
	}
	inspected, err := (torrent.Inspector{}).InspectTorrent(metainfo(validInfo()))
	if err != nil {
		t.Fatal(err)
	}
	matches, err := media.ValidateCompleteTVTorrentFiles(inspected.Files, rows, units)
	if err != nil || len(matches) != 2 || matches[0].Unit != rows[0] || matches[1].Unit != rows[1] {
		t.Fatal("exact torrent files did not retain frozen UUIDs/versions", matches, err)
	}
	expanded := validInfo()
	expanded["files"] = bl(file("Show.S01E01.mkv", 4), file("Show.S01E03.mkv", 4))
	other, err := (torrent.Inspector{}).InspectTorrent(metainfo(expanded))
	if err != nil {
		t.Fatal(err)
	}
	if other.InfoHash == inspected.InfoHash {
		t.Fatal("info hash failed to bind actual file set")
	}
	if got, err := media.ValidateCompleteTVTorrentFiles(other.Files, rows, units); got != nil || !errors.Is(err, media.ErrTVCoverageFilesRequireReview) {
		t.Fatal("changed file coverage escaped review", got, err)
	}

	for _, parent := range []string{"Show All Seasons (1-8)", "Show 3 Movie Collection", "Show.2025.part01.rar"} {
		info := validInfo()
		info["name"] = bs(parent)
		envelope, err := (torrent.Inspector{}).InspectTorrent([]byte(bd(map[string]string{"info": bd(info)})))
		if err != nil {
			t.Fatal("parent declaration is lexical evidence, not an invalid torrent", err)
		}
		if got, err := media.ValidateCompleteTVTorrentFiles(envelope.Files, rows, units); got != nil || !errors.Is(err, media.ErrTVCoverageFilesRequireReview) {
			t.Fatal("parent declaration masked frozen scope", parent, got, err)
		}
	}
}
