package postgres

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type mfaTransaction struct {
	tx   pgx.Tx
	user uuid.UUID
}

func (t *mfaTransaction) LoadMFA(ctx context.Context) (ports.MFAState, error) {
	r, e := db.New(t.tx).LoadAccountTOTP(ctx, t.user)
	if errors.Is(e, pgx.ErrNoRows) {
		return ports.MFAState{}, nil
	}
	if e != nil {
		return ports.MFAState{}, e
	}
	return ports.MFAState{Enabled: r.Enabled, Secret: r.SecretCiphertext, LastCounter: r.LastCounter, ChallengeID: r.ChallengeID, ExpiresAt: r.ExpiresAt.Time}, nil
}
func (t *mfaTransaction) StoreMFA(ctx context.Context, s ports.MFAState) error {
	return db.New(t.tx).UpsertAccountTOTP(ctx, db.UpsertAccountTOTPParams{UserID: t.user, Enabled: s.Enabled, SecretCiphertext: s.Secret, LastCounter: s.LastCounter, ChallengeID: s.ChallengeID, ExpiresAt: pgtype.Timestamptz{Time: s.ExpiresAt, Valid: true}})
}
func (t *mfaTransaction) ConsumeRecoveryCode(ctx context.Context, hash []byte) (bool, error) {
	count, e := db.New(t.tx).ConsumeAccountTOTPRecovery(ctx, db.ConsumeAccountTOTPRecoveryParams{UserID: t.user, CodeHash: hash})
	return count == 1, e
}
func (t *mfaTransaction) ReplaceRecoveryCodes(ctx context.Context, hashes [][]byte) error {
	q := db.New(t.tx)
	if e := q.DeleteAccountTOTPRecoveryCodes(ctx, t.user); e != nil {
		return e
	}
	for _, h := range hashes {
		if e := q.InsertAccountTOTPRecoveryCode(ctx, db.InsertAccountTOTPRecoveryCodeParams{UserID: t.user, CodeHash: h}); e != nil {
			return e
		}
	}
	return nil
}
func (t *mfaTransaction) MFAStatus(ctx context.Context) (ports.MFAStatus, error) {
	r, e := db.New(t.tx).ReadAccountTOTPStatus(ctx, t.user)
	return ports.MFAStatus{Enabled: r.Enabled, RecoveryCodesRemaining: int(r.RecoveryCodesRemaining)}, e
}
func (t *mfaTransaction) DeleteMFA(ctx context.Context) error {
	return db.New(t.tx).DeleteAccountTOTP(ctx, t.user)
}
