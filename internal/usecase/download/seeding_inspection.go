package download

import (
	"context"
	"encoding/hex"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type SeedingInspectionService struct {
	repository ports.SeedingInspectionRepository
	clock      ports.Clock
}

func NewSeedingInspectionService(repository ports.SeedingInspectionRepository, clock ports.Clock) *SeedingInspectionService {
	return &SeedingInspectionService{repository: repository, clock: clock}
}

func (s *SeedingInspectionService) Inspect(ctx context.Context, actor *ports.Actor, operation uuid.UUID) (*ports.SeedingInspection, error) {
	if operation == uuid.Nil {
		return nil, ports.ErrDownloadControlInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.repository == nil || s.clock == nil {
		return nil, ports.ErrUnavailable
	}
	prepared, err := s.repository.PrepareSeedingInspection(ctx, actor, operation)
	if prepared != nil && prepared.Release != nil {
		defer prepared.Release()
	}
	if err != nil {
		return nil, err
	}
	if prepared == nil || prepared.Reader == nil || prepared.Validate == nil || prepared.Release == nil {
		return nil, ports.ErrUnavailable
	}
	hash := prepared.InfoHash
	if _, err = hex.DecodeString(hash); err != nil || (len(hash) != 40 && len(hash) != 64) || hash != strings.ToLower(hash) || prepared.OperationTag != "pulsemedia:op:"+operation.String() || prepared.SavePath == "" {
		return nil, ports.ErrDownloadControlConflict
	}
	if err = prepared.Validate(ctx); err != nil {
		return nil, err
	}
	readCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	observed, err := prepared.Reader.ReadSeedingTorrent(readCtx, hash, prepared.OperationTag)
	if readCtx.Err() != nil {
		err = readCtx.Err()
	}
	cancel()
	// The budget remains held until the actual native call and final validation return.
	validationErr := prepared.Validate(ctx)
	if err != nil {
		return nil, err
	}
	if validationErr != nil {
		return nil, validationErr
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if observed == nil || observed.InfoHash != hash || observed.OperationTag != prepared.OperationTag || observed.SavePath != prepared.SavePath || observed.TotalBytes < 0 {
		return nil, ports.ErrBadResponse
	}
	facts := observed.Facts
	if facts.ShareRatio != nil && (math.IsNaN(*facts.ShareRatio) || math.IsInf(*facts.ShareRatio, 0) || *facts.ShareRatio < 0 || facts.InfiniteShareRatio) || facts.SeedingSeconds != nil && *facts.SeedingSeconds < 0 {
		return nil, ports.ErrBadResponse
	}
	out := &ports.SeedingInspection{ObservedAt: s.clock.Now(), Complete: facts.Complete, InfiniteShareRatio: facts.InfiniteShareRatio}
	if facts.ShareRatio != nil {
		value := *facts.ShareRatio
		out.ShareRatio = &value
	}
	if facts.SeedingSeconds != nil {
		value := strconv.FormatInt(*facts.SeedingSeconds, 10)
		out.SeedingSeconds = &value
	}
	return out, nil
}
