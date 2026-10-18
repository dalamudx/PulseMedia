package ports

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
)

type preparationEvidenceInspector func([]byte) (*TorrentMetainfo, error)

func (f preparationEvidenceInspector) InspectTorrent(b []byte) (*TorrentMetainfo, error) { return f(b) }
func TestCompleteTVPreparationSeal(t *testing.T) {
	// Fake inspector isolates port boundary ownership/sealing. Native PG tests
	// use the production inspector and a separately hashed literal bencode input.
	raw := []byte("owned input")
	valid := func(b []byte) (*TorrentMetainfo, error) {
		return &TorrentMetainfo{TorrentBytes: bytes.Clone(b), BytesSHA256: sha256.Sum256(b), InfoHash: strings.Repeat("a", 40), TotalBytes: 2, Files: []media.TVTorrentFile{{Path: "Show/S01E01.mkv", Size: 1}, {Path: "Show/S01E02.mkv", Size: 1}}}, nil
	}
	for name, mutate := range map[string]func(*CompleteTVMetainfoProof){
		"bytes":     func(p *CompleteTVMetainfoProof) { p.raw[0] = 'x' },
		"digest":    func(p *CompleteTVMetainfoProof) { p.digest[0]++ },
		"hash":      func(p *CompleteTVMetainfoProof) { p.hash = strings.Repeat("b", 40) },
		"total":     func(p *CompleteTVMetainfoProof) { p.total++ },
		"file-path": func(p *CompleteTVMetainfoProof) { p.files[0].Path = "injected" },
		"file-size": func(p *CompleteTVMetainfoProof) { p.files[0].Size++ },
	} {
		t.Run(name, func(t *testing.T) {
			p, e := InspectCompleteTVMetainfo(preparationEvidenceInspector(valid), raw)
			if e != nil || !p.ValidateBinding() {
				t.Fatal(e)
			}
			mutate(p)
			if p.ValidateBinding() {
				t.Fatal("altered evidence accepted")
			}
		})
	}
	for name, mutate := range map[string]func(*TorrentMetainfo){
		"digest":      func(p *TorrentMetainfo) { p.BytesSHA256[0]++ },
		"other-bytes": func(p *TorrentMetainfo) { p.TorrentBytes = []byte("other input") },
		"hash-format": func(p *TorrentMetainfo) { p.InfoHash = "bad" },
	} {
		t.Run("inspector-"+name, func(t *testing.T) {
			inspector := preparationEvidenceInspector(func(b []byte) (*TorrentMetainfo, error) { p, _ := valid(b); mutate(p); return p, nil })
			if p, e := InspectCompleteTVMetainfo(inspector, raw); p != nil || !errors.Is(e, ErrCompleteTVSubmissionReview) {
				t.Fatal(p, e)
			}
		})
	}
}
