package ports

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/google/uuid"
	"strings"
)

var ErrCompleteTVSubmissionReview = errors.New("complete TV preparation requires review; protection retained")

// CompleteTVMetainfoProof owns the original bytes. Its private evidence can only
// be constructed through the injected inspector, outside the repository transaction.
// It cannot be deserialized from caller file claims or a persisted JSON body.
type CompleteTVMetainfoProof struct {
	raw    []byte
	digest [32]byte
	hash   string
	total  int64
	files  []media.TVTorrentFile
	seal   [32]byte
}

func InspectCompleteTVMetainfo(inspector TorrentMetainfoInspector, input []byte) (*CompleteTVMetainfoProof, error) {
	if inspector == nil || len(input) == 0 || len(input) > MaxTorrentMetainfoBytes {
		return nil, ErrCompleteTVSubmissionReview
	}
	raw := bytes.Clone(input)
	m, e := inspector.InspectTorrent(bytes.Clone(raw))
	if e != nil || m == nil || !bytes.Equal(m.TorrentBytes, raw) || m.BytesSHA256 != sha256.Sum256(raw) || m.TotalBytes < 1 || len(m.Files) < 2 || len(m.Files) > 4096 {
		return nil, ErrCompleteTVSubmissionReview
	}
	h, e := hex.DecodeString(m.InfoHash)
	if e != nil || len(h) != 20 || strings.ToLower(m.InfoHash) != m.InfoHash {
		return nil, ErrCompleteTVSubmissionReview
	}
	p := &CompleteTVMetainfoProof{raw: raw, digest: m.BytesSHA256, hash: m.InfoHash, total: m.TotalBytes, files: append([]media.TVTorrentFile(nil), m.Files...)}
	p.seal = p.evidenceSeal()
	return p, nil
}
func (p *CompleteTVMetainfoProof) evidenceSeal() [32]byte {
	b, _ := json.Marshal([]any{"complete-tv.metainfo.v1", p.digest, p.hash, p.total, p.files})
	return sha256.Sum256(b)
}

// ValidateBinding checks the owned bytes and sealed evidence again at preparation.
func (p *CompleteTVMetainfoProof) ValidateBinding() bool {
	return p != nil && len(p.raw) > 0 && len(p.raw) <= MaxTorrentMetainfoBytes && sha256.Sum256(p.raw) == p.digest && p.seal == p.evidenceSeal()
}
func (p *CompleteTVMetainfoProof) Bytes() []byte     { return bytes.Clone(p.raw) }
func (p *CompleteTVMetainfoProof) SHA256() [32]byte  { return p.digest }
func (p *CompleteTVMetainfoProof) InfoHash() string  { return p.hash }
func (p *CompleteTVMetainfoProof) TotalBytes() int64 { return p.total }
func (p *CompleteTVMetainfoProof) Files() []media.TVTorrentFile {
	return append([]media.TVTorrentFile(nil), p.files...)
}

type PrepareCompleteTVSubmissionCommand struct {
	Lease               JobLease
	TaskID, OperationID uuid.UUID
	Metainfo            *CompleteTVMetainfoProof `json:"-"`
}
type CompleteTVSubmissionReceipt struct {
	TaskID         uuid.UUID `json:"task_id"`
	State          string    `json:"state"`
	AlreadyExisted bool      `json:"already_existed"`
}
type CompleteTVSubmissionRepository interface {
	PrepareCompleteTVSubmission(context.Context, PrepareCompleteTVSubmissionCommand) (*CompleteTVSubmissionReceipt, error)
}
