package ports_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

type automaticTVInspectorFunc func([]byte) (*ports.TorrentMetainfo, error)

func (f automaticTVInspectorFunc) InspectTorrent(raw []byte) (*ports.TorrentMetainfo, error) {
	return f(raw)
}

func automaticTVMetainfo(raw []byte) *ports.TorrentMetainfo {
	return &ports.TorrentMetainfo{
		TorrentBytes: bytes.Clone(raw), BytesSHA256: sha256.Sum256(raw),
		InfoHash: strings.Repeat("ab", 20), TotalBytes: 7,
		Files: []media.TVTorrentFile{{Path: "Fixture.2020.S01E03.mkv", Size: 7}},
	}
}

func TestAutomaticTVMetainfoOwnedEvidence(t *testing.T) {
	raw := []byte("private tracker passkey evidence")
	original := bytes.Clone(raw)
	var borrowedInput []byte
	var returned *ports.TorrentMetainfo
	inspector := automaticTVInspectorFunc(func(input []byte) (*ports.TorrentMetainfo, error) {
		borrowedInput = input
		returned = automaticTVMetainfo(input)
		returned.Files = append(returned.Files, media.TVTorrentFile{Path: "poster.jpg"})
		return returned, nil
	})
	proof, err := ports.InspectAutomaticTVMetainfo(inspector, raw)
	if err != nil || proof == nil || !proof.ValidateBinding() {
		t.Fatal("valid evidence rejected", err)
	}
	wantFiles := append([]media.TVTorrentFile(nil), returned.Files...)
	raw[0], borrowedInput[1], returned.TorrentBytes[2] = 'x', 'y', 'z'
	returned.BytesSHA256 = [32]byte{}
	returned.InfoHash, returned.TotalBytes = "changed", 100
	returned.Files[0] = media.TVTorrentFile{Path: "other", Size: 900}
	copyBytes, copyFiles := proof.Bytes(), proof.Files()
	copyBytes[0] = '!'
	copyFiles[0].Path, copyFiles[0].Size = "changed", -1
	if !proof.ValidateBinding() || !bytes.Equal(proof.Bytes(), original) || proof.SHA256() != sha256.Sum256(original) ||
		proof.InfoHash() != strings.Repeat("ab", 20) || proof.TotalBytes() != 7 || !reflect.DeepEqual(proof.Files(), wantFiles) {
		t.Fatal("proof aliases mutable caller or inspector evidence")
	}
}

func TestAutomaticTVMetainfoInspectorMutation(t *testing.T) {
	for _, preserveOriginal := range []bool{false, true} {
		t.Run(map[bool]string{false: "mutated result rejected", true: "original result remains owned"}[preserveOriginal], func(t *testing.T) {
			raw := []byte("original")
			proof, err := ports.InspectAutomaticTVMetainfo(automaticTVInspectorFunc(func(input []byte) (*ports.TorrentMetainfo, error) {
				m := automaticTVMetainfo(input)
				input[0] = '!'
				if !preserveOriginal {
					m.TorrentBytes = input
					m.BytesSHA256 = sha256.Sum256(input)
				}
				return m, nil
			}), raw)
			if string(raw) != "original" {
				t.Fatal("inspector mutated caller input")
			}
			if preserveOriginal {
				if err != nil || proof == nil || !proof.ValidateBinding() || string(proof.Bytes()) != "original" {
					t.Fatal("separate original evidence was lost", err)
				}
			} else if proof != nil || err != ports.ErrAutomaticTVMetainfoReview {
				t.Fatal("mutated inspector result accepted")
			}
		})
	}
}

func TestAutomaticTVMetainfoRejectsInvalidClaims(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*ports.TorrentMetainfo)
	}{
		{"different raw", func(m *ports.TorrentMetainfo) { m.TorrentBytes[0] ^= 1 }},
		{"missing raw", func(m *ports.TorrentMetainfo) { m.TorrentBytes = nil }},
		{"fake digest", func(m *ports.TorrentMetainfo) { m.BytesSHA256 = [32]byte{} }},
		{"uppercase hash", func(m *ports.TorrentMetainfo) { m.InfoHash = strings.ToUpper(m.InfoHash) }},
		{"short hash", func(m *ports.TorrentMetainfo) { m.InfoHash = strings.Repeat("a", 39) }},
		{"long hash", func(m *ports.TorrentMetainfo) { m.InfoHash += "00" }},
		{"non hex hash", func(m *ports.TorrentMetainfo) { m.InfoHash = strings.Repeat("z", 40) }},
		{"empty hash", func(m *ports.TorrentMetainfo) { m.InfoHash = "" }},
		{"zero total", func(m *ports.TorrentMetainfo) { m.TotalBytes = 0 }},
		{"negative total", func(m *ports.TorrentMetainfo) { m.TotalBytes = -1 }},
		{"total mismatch", func(m *ports.TorrentMetainfo) { m.TotalBytes++ }},
		{"nil files", func(m *ports.TorrentMetainfo) { m.Files = nil }},
		{"too many files", func(m *ports.TorrentMetainfo) { m.Files = make([]media.TVTorrentFile, 4097); m.Files[0].Size = 7 }},
		{"negative size", func(m *ports.TorrentMetainfo) {
			m.Files = append(m.Files, media.TVTorrentFile{Size: -1})
			m.TotalBytes = 6
		}},
		{"overflow sum", func(m *ports.TorrentMetainfo) {
			m.Files = []media.TVTorrentFile{{Size: math.MaxInt64}, {Size: 1}}
			m.TotalBytes = math.MaxInt64
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ports.InspectAutomaticTVMetainfo(automaticTVInspectorFunc(func(raw []byte) (*ports.TorrentMetainfo, error) {
				m := automaticTVMetainfo(raw)
				tc.mutate(m)
				return m, nil
			}), []byte("secret passkey raw"))
			if p != nil || !errors.Is(err, ports.ErrAutomaticTVMetainfoReview) || err.Error() != ports.ErrAutomaticTVMetainfoReview.Error() {
				t.Fatal("unsafe or accepted invalid claims")
			}
		})
	}
}

func TestAutomaticTVMetainfoInputAndInspectorFailures(t *testing.T) {
	for _, tc := range []struct {
		name      string
		input     []byte
		inspector ports.TorrentMetainfoInspector
	}{
		{"nil inspector", []byte("x"), nil},
		{"typed nil inspector", []byte("x"), automaticTVInspectorFunc(nil)},
		{"nil input", nil, automaticTVInspectorFunc(func([]byte) (*ports.TorrentMetainfo, error) { t.Fatal("inspected nil input"); return nil, nil })},
		{"empty input", []byte{}, automaticTVInspectorFunc(func([]byte) (*ports.TorrentMetainfo, error) { t.Fatal("inspected empty input"); return nil, nil })},
		{"oversized input", make([]byte, ports.MaxTorrentMetainfoBytes+1), automaticTVInspectorFunc(func([]byte) (*ports.TorrentMetainfo, error) { t.Fatal("inspected oversized input"); return nil, nil })},
		{"nil result", []byte("secret"), automaticTVInspectorFunc(func([]byte) (*ports.TorrentMetainfo, error) { return nil, nil })},
		{"error", []byte("secret"), automaticTVInspectorFunc(func([]byte) (*ports.TorrentMetainfo, error) { return nil, errors.New("private passkey path") })},
		{"result and error", []byte("secret"), automaticTVInspectorFunc(func(b []byte) (*ports.TorrentMetainfo, error) {
			return automaticTVMetainfo(b), errors.New("private tracker")
		})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ports.InspectAutomaticTVMetainfo(tc.inspector, tc.input)
			if p != nil || err != ports.ErrAutomaticTVMetainfoReview {
				t.Fatal("failure returned evidence or leaked error")
			}
		})
	}
}

func TestAutomaticTVMetainfoInclusiveBounds(t *testing.T) {
	for _, length := range []int{1, ports.MaxTorrentMetainfoBytes} {
		raw := make([]byte, length)
		p, err := ports.InspectAutomaticTVMetainfo(automaticTVInspectorFunc(func(b []byte) (*ports.TorrentMetainfo, error) {
			m := automaticTVMetainfo(b)
			m.Files = make([]media.TVTorrentFile, 4096)
			m.Files[0].Size, m.TotalBytes = math.MaxInt64, math.MaxInt64
			return m, nil
		}), raw)
		if err != nil || p == nil || !p.ValidateBinding() || len(p.Bytes()) != length || len(p.Files()) != 4096 {
			t.Fatal("inclusive byte/file/size bound rejected", err)
		}
	}
}

//nolint:staticcheck // SA9005 is intentional: verify private evidence cannot be encoded or supplied through JSON.
func TestAutomaticTVMetainfoCannotDeserializeClaims(t *testing.T) {
	p, err := ports.InspectAutomaticTVMetainfo(automaticTVInspectorFunc(func(b []byte) (*ports.TorrentMetainfo, error) { return automaticTVMetainfo(b), nil }), []byte("private passkey"))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(p)
	if err != nil || string(encoded) != "{}" {
		t.Fatal("proof exposed fields")
	}
	var decoded ports.AutomaticTVMetainfoProof
	if err := json.Unmarshal([]byte(`{"raw":"c2VjcmV0","digest":[1],"hash":"abababababababababababababababababababab","total":7,"files":[{"Path":"Fixture.2020.S01E03.mkv","Size":7}],"seal":[1]}`), &decoded); err != nil || decoded.ValidateBinding() {
		t.Fatal("caller claims created valid proof", err)
	}
	if decoded.Bytes() != nil || decoded.Files() != nil || decoded.InfoHash() != "" || decoded.TotalBytes() != 0 || decoded.SHA256() != [32]byte{} {
		t.Fatal("JSON populated private evidence")
	}
}

func TestAutomaticTVMetainfoNilAndZeroMethods(t *testing.T) {
	var nilProof *ports.AutomaticTVMetainfoProof
	for _, p := range []*ports.AutomaticTVMetainfoProof{nilProof, {}} {
		if p.ValidateBinding() || p.Bytes() != nil || p.Files() != nil || p.SHA256() != [32]byte{} || p.InfoHash() != "" || p.TotalBytes() != 0 {
			t.Fatal("nil/zero proof is valid or not safe")
		}
	}
}
