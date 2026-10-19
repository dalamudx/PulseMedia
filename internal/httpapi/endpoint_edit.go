package httpapi

import (
	"errors"

	"github.com/dalamudx/PulseMedia/internal/domain/endpoint"
)

var (
	errEndpointRevision = errors.New("endpoint revision changed or missing")
	errEndpointMasked   = errors.New("endpoint URL contains a display placeholder")
)

// The expected revision and raw URL are read under the same row lock as the
// update. Equal projections alone cannot distinguish two different secrets.
func resolveEndpointEditURL(submitted, current string, hasVersion, versionMatches bool) (string, error) {
	if !hasVersion || !versionMatches {
		return "", errEndpointRevision
	}
	display := endpointDisplayURL(current)
	if submitted == display && display != current {
		return current, nil
	}
	if endpointURLHasMask(submitted) {
		return "", errEndpointMasked
	}
	return submitted, nil
}

func endpointURLHasMask(raw string) bool { return endpoint.URLHasMask(raw) }
