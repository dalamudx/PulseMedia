package quality

import "encoding/json"

// This is the version of filename HDR evidence, not a quality rank or file identity.
const HDRTokenParserVersion = "hdr.tokens.v1"
const ResolutionTokenParserVersion = "resolution.tokens.v1"
const SourceTokenParserVersion = "source.tokens.v2"
const CodecTokenParserVersion = "codec.tokens.v2"

func VerifiedHDR(p Profile) bool {
	return p.HDRParserVersion == HDRTokenParserVersion && (p.HDR == HDRDolbyVision || p.HDR == HDR10Plus || p.HDR == HDR10 || p.HDR == HDRSDR)
}

// Decode legacy/unsupported filename evidence as unknown without rewriting its
// persisted JSON, file facts, history, or physical reservations. No measurement
// adapter currently supplies a separate HDR evidence contract.
func (p *Profile) UnmarshalJSON(data []byte) error {
	type plain Profile
	var value plain
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*p = Profile(value)
	if p.ResolutionParserVersion != ResolutionTokenParserVersion {
		p.Resolution = ""
		p.ResolutionParserVersion = ""
	}
	if p.SourceParserVersion != SourceTokenParserVersion {
		p.Source = ""
		p.SourceParserVersion = ""
	}
	if p.CodecParserVersion != CodecTokenParserVersion {
		p.Codec = ""
		p.CodecParserVersion = ""
	}
	if !VerifiedHDR(*p) {
		p.HDR = ""
		p.HDRParserVersion = ""
	}
	return nil
}
