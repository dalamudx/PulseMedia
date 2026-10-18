package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type preparationProofCapture struct {
	raw       []byte
	sha       [32]byte
	infoHash  string
	total     int64
	proofJSON json.RawMessage
}

func (s *UpgradePreparationStore) capturePreparationProof(
	ctx context.Context, tx pgx.Tx, b *preparationBasis,
	proof ports.MovieUpgradeMetainfoProof, initialRaw []byte,
) (*preparationProofCapture, error) {
	// Verification is mandatory even if the interface reports valid binding.
	if proof == nil {
		return nil, preparationConflict()
	}
	if err := s.verifier.VerifyMovieUpgradeMetainfo(ctx, proof, b.target); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, preparationConflict()
	}
	raw := append([]byte(nil), proof.Bytes()...)
	sum := sha256.Sum256(raw)
	if len(raw) == 0 || len(raw) > 10<<20 ||
		sha256.Sum256(initialRaw) != sum ||
		proof.SHA256() != sum || !proof.ValidateBinding() {
		return nil, preparationConflict()
	}
	targetRaw, err := json.Marshal(b.target)
	if err != nil {
		return nil, err
	}
	proofTarget, err := json.Marshal(proof.Target())
	if err != nil {
		return nil, err
	}
	if !preparationSameJSON(targetRaw, proofTarget) {
		return nil, preparationConflict()
	}
	files := proof.Files()
	video := proof.Video()
	total := proof.TotalBytes()
	infoHash := strings.ToLower(proof.InfoHash())
	if len(files) == 0 || total <= 0 || len(infoHash) != 40 {
		return nil, preparationConflict()
	}
	if _, err = hex.DecodeString(infoHash); err != nil {
		return nil, preparationConflict()
	}
	// The native verifier owns the complete file/unique-video/identity rules.
	// Also bind a persisted reference when the candidate already has one.
	candidate, err := db.New(tx).GetCandidateByID(ctx, b.snapshot.CandidateID)
	if err != nil {
		return nil, err
	}
	if candidate.InfoHash.Valid &&
		strings.ToLower(candidate.InfoHash.String) != infoHash {
		return nil, preparationConflict()
	}
	view, err := json.Marshal(map[string]any{
		"sha256":    hex.EncodeToString(sum[:]),
		"info_hash": infoHash, "total_bytes": total,
		"files": files, "video": video, "target": b.target,
	})
	if err != nil {
		return nil, err
	}
	return &preparationProofCapture{
		raw: raw, sha: sum, infoHash: infoHash, total: total, proofJSON: view,
	}, nil
}

func (b *preparationBasis) evidence(
	proof *preparationProofCapture,
) (json.RawMessage, error) {
	return json.Marshal(map[string]any{
		"kind":              "movie_upgrade_preparation_v1",
		"operation_id":      b.operation,
		"authority":         b.authority,
		"protected_digest":  b.snapshot.ProtectedDigest,
		"root_id":           b.snapshot.Observation.RootID,
		"root_generation":   b.snapshot.RootGeneration,
		"client_id":         b.snapshot.ClientID,
		"client_digest":     b.snapshot.ClientDigest,
		"save_path":         b.savePath,
		"old_file_id":       b.snapshot.CurrentFileID,
		"old_fact_id":       b.snapshot.CurrentFactID,
		"frozen_identities": b.snapshot.FrozenIdentities,
		"lane":              b.lane,
		"reservation":       b.reserved,
		"requirement":       b.required,
		"raw_sha256":        hex.EncodeToString(proof.sha[:]),
		"proof":             proof.proofJSON,
	})
}

func insertMovieUpgradePreparation(
	ctx context.Context, tx pgx.Tx, b *preparationBasis,
	task uuid.UUID, key, hash string, c ports.PrepareMovieUpgrade,
	proof *preparationProofCapture, evidence, taskSnapshot json.RawMessage,
) (json.RawMessage, error) {
	// Lock acquisition and all source reads have already completed. This
	// timestamp is taken at the actual preparation write, not transaction start.
	var actual json.RawMessage
	var valid bool
	err := tx.QueryRow(ctx, `
WITH t AS (SELECT clock_timestamp() AS now),
written AS (
 INSERT INTO movie_upgrade_preparation(
  operation_id,task_id,actor_id,request_key,request_hash,owner,token,
  status,raw_sha256,evidence,task_snapshot,prepared_at
 )
 SELECT $1,$2,$3,$4,$5,$6,$7,'prepared_not_sent',
        $8,$9::jsonb,$10::jsonb,t.now
 FROM t JOIN movie_upgrade_preparation_lease l ON l.operation_id=$1
 WHERE l.actor_id=$3 AND l.owner=$6 AND l.token=$7
  AND l.state='leased' AND l.lease_until>t.now
 RETURNING *
)
SELECT to_jsonb(w),
 w.operation_id=$1 AND w.task_id=$2 AND w.actor_id=$3
 AND w.request_key=$4 AND w.request_hash=$5
 AND w.owner=$6 AND w.token=$7 AND w.status='prepared_not_sent'
 AND w.raw_sha256=$8
 AND w.evidence=$9::jsonb AND w.task_snapshot=$10::jsonb
 AND w.prepared_at=l.now
FROM written w CROSS JOIN t l`,
		b.operation, task, uuid.MustParse(b.actor.ID), key, hash,
		c.Owner, c.Token, proof.sha[:], evidence, taskSnapshot,
	).Scan(&actual, &valid)
	if err != nil {
		return nil, preparationRequiredRow(err)
	}
	if !valid {
		return nil, preparationConflict()
	}
	return actual, nil
}
