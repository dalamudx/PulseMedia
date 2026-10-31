package ports

import (
	"context"
	"errors"
	"github.com/google/uuid"
)

var ErrDownloadControlConflict = errors.New("download control requires review")
var ErrDownloadControlInvalid = errors.New("invalid download control command")
var ErrDownloadControlUnsupported = errors.New("native download control unavailable")

type DownloadPauseObservation struct {
	Info   *TorrentInfo
	Paused bool
}

// Optional native capability; it never selects another endpoint or torrent.
type NativeDownloadControl interface {
	SetTorrentPaused(context.Context, string, bool) error
	ReadTorrentPaused(context.Context, string) (*DownloadPauseObservation, error)
}
type DownloadControlCommand struct {
	Action       string
	Confirmed    bool
	AffectShared bool
}
type DownloadControlReceipt struct {
	CommandID   uuid.UUID `json:"command_id"`
	OperationID uuid.UUID `json:"operation_id"`
	Action      string    `json:"action"`
	State       string    `json:"state"`
}

// Arm persists the one physical-send permit after the exact native identity read.
// Complete records actual return even after cancellation; Release runs only last.
type PreparedDownloadControl struct {
	Receipt  *DownloadControlReceipt
	Client   NativeDownloadControl
	InfoHash string
	Arm      func(context.Context, *DownloadPauseObservation) error
	Complete func(context.Context, error, *DownloadPauseObservation) (*DownloadControlReceipt, error)
	Release  func()
}
type DownloadControlRepository interface {
	PrepareDownloadControl(context.Context, *Actor, uuid.UUID, string, DownloadControlCommand, bool) (*PreparedDownloadControl, error)
	GetDownloadControl(context.Context, *Actor, uuid.UUID, string) (*DownloadControlReceipt, error)
}
