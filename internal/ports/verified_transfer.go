package ports

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

var ErrFileUnsupported = errors.New("filesystem operation is unsupported")
var ErrFileNoSpace = errors.New("filesystem has insufficient space")

// TransferContent binds the full content digest to an observed regular file.
// A size or partial digest is never completion evidence.
type TransferContent struct {
	File   FileIdentity
	SHA256 [32]byte
}

type VerifiedTransferResult struct {
	Target        TransferContent
	SourceRemoved bool
}

// VerifiedTransferFilesystem is required only for explicitly selected copy,
// reflink and move actions. Existing hardlink callers keep TransferFilesystem.
// Callers hold both physical root slots until every operation returns and persist
// the source content and stable step ID before publication. Recovery verifies the
// complete target against that frozen digest; move also requires source absence.
type VerifiedTransferFilesystem interface {
	TransferFilesystem
	InspectContent(context.Context, TransferRoot, string, FileIdentity) (TransferContent, error)
	ProbeReflink(context.Context, TransferRoot, string, TransferRoot, FileIdentity, uuid.UUID) (bool, error)
	TransferVerified(context.Context, TransferRoot, string, TransferRoot, string, TransferContent, string, uuid.UUID) (VerifiedTransferResult, error)
	ObserveTransfer(context.Context, TransferRoot, string, TransferRoot, string, TransferContent, string, uuid.UUID) (VerifiedTransferResult, error)
}
