package ports

import (
	"errors"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
)

var ErrTVEpisodeUpgradeMetainfoReview = errors.New("TV episode upgrade metainfo requires review; protection retained")

// TVEpisodeUpgradeMetainfoProof owns inspected bytes and their single-episode
// binding. The inspector is a trusted dependency and must be the native inspector
// in production. This evidence grants no send, replacement or retirement authority.
type TVEpisodeUpgradeMetainfoProof struct {
	metainfo *AutomaticTVMetainfoProof
	target   media.TVEpisodeUpgradeTarget
	video    media.TVTorrentUnitFile
}

func InspectTVEpisodeUpgradeMetainfo(inspector TorrentMetainfoInspector, input []byte, target media.TVEpisodeUpgradeTarget) (*TVEpisodeUpgradeMetainfoProof, error) {
	m, err := InspectAutomaticTVMetainfo(inspector, input)
	if err != nil {
		return nil, ErrTVEpisodeUpgradeMetainfoReview
	}
	video, err := media.ValidateTVEpisodeUpgradeTorrentFiles(m.Files(), target)
	if err != nil {
		return nil, ErrTVEpisodeUpgradeMetainfoReview
	}
	return &TVEpisodeUpgradeMetainfoProof{metainfo: m, target: target, video: *video}, nil
}

func (p *TVEpisodeUpgradeMetainfoProof) ValidateBinding() bool {
	if p == nil || p.metainfo == nil || !p.metainfo.ValidateBinding() {
		return false
	}
	video, err := media.ValidateTVEpisodeUpgradeTorrentFiles(p.metainfo.Files(), p.target)
	return err == nil && *video == p.video
}

func (p *TVEpisodeUpgradeMetainfoProof) Bytes() []byte {
	if p == nil {
		return nil
	}
	return p.metainfo.Bytes()
}

func (p *TVEpisodeUpgradeMetainfoProof) SHA256() [32]byte {
	if p == nil {
		return [32]byte{}
	}
	return p.metainfo.SHA256()
}

func (p *TVEpisodeUpgradeMetainfoProof) InfoHash() string {
	if p == nil {
		return ""
	}
	return p.metainfo.InfoHash()
}

func (p *TVEpisodeUpgradeMetainfoProof) TotalBytes() int64 {
	if p == nil {
		return 0
	}
	return p.metainfo.TotalBytes()
}

func (p *TVEpisodeUpgradeMetainfoProof) Files() []media.TVTorrentFile {
	if p == nil {
		return nil
	}
	return p.metainfo.Files()
}

func (p *TVEpisodeUpgradeMetainfoProof) Target() media.TVEpisodeUpgradeTarget {
	if p == nil {
		return media.TVEpisodeUpgradeTarget{}
	}
	return p.target
}

func (p *TVEpisodeUpgradeMetainfoProof) Video() media.TVTorrentUnitFile {
	if p == nil {
		return media.TVTorrentUnitFile{}
	}
	return p.video
}
