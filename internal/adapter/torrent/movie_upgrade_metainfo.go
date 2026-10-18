package torrent

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

var ErrMovieUpgradeMetainfoReview = ports.ErrMovieUpgradeMetainfoReview

// MovieUpgradeMetainfoProof is owned file evidence, not submission/upgrade
// authority. Only the native v1 decoder can issue one. In particular an injected
// TorrentMetainfoInspector's caller-controlled DTO cannot mint movie evidence.
// It is intentionally not a CompleteTVMetainfoProof and has no execution API.
// Usecases consume the ports proof view and injected native verifier; they must
// never import this adapter or treat a caller-supplied validation bool as evidence.
type MovieUpgradeMetainfoProof struct {
	raw    []byte
	digest [32]byte
	hash   string
	total  int64
	files  []media.TVTorrentFile
	target media.MovieUpgradeTarget
	video  media.MovieUpgradeUnitFile
	seal   [32]byte
	issued bool
}

func cloneMovieUpgradeTarget(t media.MovieUpgradeTarget) media.MovieUpgradeTarget {
	t.Identities = append([]media.Identity(nil), t.Identities...)
	t.Aliases = append([]string(nil), t.Aliases...)
	return t
}

// InspectMovieUpgradeMetainfo accepts bytes already fetched within a bounded
// network contract and a server-frozen target. Neither input grants execution.
// It has no I/O and never fetches a URL, re-encodes info or calls Add.
func InspectMovieUpgradeMetainfo(input []byte, target media.MovieUpgradeTarget) (*MovieUpgradeMetainfoProof, error) {
	if len(input) == 0 || len(input) > ports.MaxTorrentMetainfoBytes {
		return nil, ErrMovieUpgradeMetainfoReview
	}
	target = cloneMovieUpgradeTarget(target)
	m, err := (Inspector{}).InspectTorrent(input)
	if err != nil {
		return nil, ErrMovieUpgradeMetainfoReview
	}
	video, err := media.ValidateMovieUpgradeTorrentFiles(m.Files, target)
	if err != nil {
		return nil, ErrMovieUpgradeMetainfoReview
	}
	p := &MovieUpgradeMetainfoProof{raw: m.TorrentBytes, digest: m.BytesSHA256, hash: m.InfoHash, total: m.TotalBytes, files: m.Files, target: target, video: *video, issued: true}
	p.seal = p.evidenceSeal()
	return p, nil
}

func (p *MovieUpgradeMetainfoProof) evidenceSeal() [32]byte {
	b, _ := json.Marshal([]any{"movie-upgrade.metainfo.v1", media.MovieUpgradeFilesVersion, media.EpisodeParserVersion, p.digest, p.hash, p.total, p.files, p.target, p.video})
	return sha256.Sum256(b)
}

// ValidateBinding authenticates this in-memory issuance and its immutable owned
// evidence. It says nothing about current DB versions, lease, policy or permission.
func (p *MovieUpgradeMetainfoProof) ValidateBinding() bool {
	return p != nil && p.issued && len(p.raw) > 0 && len(p.raw) <= ports.MaxTorrentMetainfoBytes && sha256.Sum256(p.raw) == p.digest && p.seal == p.evidenceSeal()
}
func (p *MovieUpgradeMetainfoProof) Bytes() []byte {
	if !p.ValidateBinding() {
		return nil
	}
	return bytes.Clone(p.raw)
}
func (p *MovieUpgradeMetainfoProof) SHA256() [32]byte {
	if !p.ValidateBinding() {
		return [32]byte{}
	}
	return p.digest
}
func (p *MovieUpgradeMetainfoProof) InfoHash() string {
	if !p.ValidateBinding() {
		return ""
	}
	return p.hash
}
func (p *MovieUpgradeMetainfoProof) TotalBytes() int64 {
	if !p.ValidateBinding() {
		return 0
	}
	return p.total
}
func (p *MovieUpgradeMetainfoProof) Files() []media.TVTorrentFile {
	if !p.ValidateBinding() {
		return nil
	}
	return append([]media.TVTorrentFile(nil), p.files...)
}
func (p *MovieUpgradeMetainfoProof) Target() media.MovieUpgradeTarget {
	if !p.ValidateBinding() {
		return media.MovieUpgradeTarget{}
	}
	return cloneMovieUpgradeTarget(p.target)
}
func (p *MovieUpgradeMetainfoProof) Video() media.MovieUpgradeUnitFile {
	if !p.ValidateBinding() {
		return media.MovieUpgradeUnitFile{}
	}
	return p.video
}
