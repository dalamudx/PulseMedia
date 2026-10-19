package ports

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"reflect"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
)

var ErrAutomaticTVMetainfoReview = errors.New("automatic TV metainfo requires review; protection retained")

// AutomaticTVMetainfoProof owns inspected file evidence, not permission to send
// or ingest. Private fields prevent JSON claims from constructing a valid proof.
type AutomaticTVMetainfoProof struct {
	raw    []byte
	digest [32]byte
	hash   string
	total  int64
	files  []media.TVTorrentFile
	seal   [32]byte
}

func InspectAutomaticTVMetainfo(inspector TorrentMetainfoInspector, input []byte) (*AutomaticTVMetainfoProof, error) {
	if inspector == nil || len(input) == 0 || len(input) > MaxTorrentMetainfoBytes {
		return nil, ErrAutomaticTVMetainfoReview
	}
	// An interface can hold a typed nil inspector (including a function adapter).
	v := reflect.ValueOf(inspector)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if v.IsNil() {
			return nil, ErrAutomaticTVMetainfoReview
		}
	}
	raw := bytes.Clone(input)
	m, err := inspector.InspectTorrent(bytes.Clone(raw))
	if err != nil || m == nil || !bytes.Equal(m.TorrentBytes, raw) {
		return nil, ErrAutomaticTVMetainfoReview
	}
	p := &AutomaticTVMetainfoProof{
		raw: raw, digest: m.BytesSHA256, hash: m.InfoHash, total: m.TotalBytes,
		files: append([]media.TVTorrentFile(nil), m.Files...),
	}
	if !p.validEvidence() {
		return nil, ErrAutomaticTVMetainfoReview
	}
	p.seal = p.evidenceSeal()
	return p, nil
}

func (p *AutomaticTVMetainfoProof) validEvidence() bool {
	if p == nil || len(p.raw) == 0 || len(p.raw) > MaxTorrentMetainfoBytes ||
		sha256.Sum256(p.raw) != p.digest || p.total < 1 || len(p.files) < 1 || len(p.files) > 4096 ||
		len(p.hash) != 40 || strings.ToLower(p.hash) != p.hash {
		return false
	}
	if _, err := hex.DecodeString(p.hash); err != nil {
		return false
	}
	var total int64
	for _, f := range p.files {
		if f.Size < 0 || f.Size > math.MaxInt64-total {
			return false
		}
		total += f.Size
	}
	return total == p.total
}

func (p *AutomaticTVMetainfoProof) evidenceSeal() [32]byte {
	// Length-prefixed strings preserve exact path bytes, including invalid UTF-8
	// that the independent domain file validator must reject.
	b := []byte("automatic-tv.metainfo.v1")
	b = append(b, p.digest[:]...)
	b = binary.AppendUvarint(b, uint64(len(p.hash)))
	b = append(b, p.hash...)
	b = binary.AppendUvarint(b, uint64(p.total))
	b = binary.AppendUvarint(b, uint64(len(p.files)))
	for _, f := range p.files {
		b = binary.AppendUvarint(b, uint64(len(f.Path)))
		b = append(b, f.Path...)
		b = binary.AppendUvarint(b, uint64(f.Size))
	}
	return sha256.Sum256(b)
}

// ValidateBinding rechecks the exact owned bytes and all sealed file evidence.
// Prepare must separately verify current authority, dates, lease and source.
func (p *AutomaticTVMetainfoProof) ValidateBinding() bool {
	return p.validEvidence() && p.seal == p.evidenceSeal()
}

func (p *AutomaticTVMetainfoProof) Bytes() []byte {
	if p == nil {
		return nil
	}
	return bytes.Clone(p.raw)
}

func (p *AutomaticTVMetainfoProof) SHA256() [32]byte {
	if p == nil {
		return [32]byte{}
	}
	return p.digest
}

func (p *AutomaticTVMetainfoProof) InfoHash() string {
	if p == nil {
		return ""
	}
	return p.hash
}

func (p *AutomaticTVMetainfoProof) TotalBytes() int64 {
	if p == nil {
		return 0
	}
	return p.total
}

func (p *AutomaticTVMetainfoProof) Files() []media.TVTorrentFile {
	if p == nil {
		return nil
	}
	return append([]media.TVTorrentFile(nil), p.files...)
}
