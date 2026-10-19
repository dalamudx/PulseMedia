package httpapi

import "github.com/dalamudx/PulseMedia/internal/domain/endpoint"

func endpointDisplayURL(raw string) string { return endpoint.DisplayURL(raw) }
