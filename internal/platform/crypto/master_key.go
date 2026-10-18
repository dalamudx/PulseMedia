package crypto

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/dalamudx/PulseMedia/internal/platform/rootfile"
)

var ErrMasterKeyState = errors.New("master key file is invalid or unavailable")

// loadOrCreateMasterKey publishes a fully persisted key without replacing a
// pre-existing file. All accesses remain relative to the opened data root.
func loadOrCreateMasterKey(dataDir string) (key []byte, err error) {
	root, err := rootfile.OpenDirectory(dataDir, true)
	if err != nil {
		return nil, fmt.Errorf("cannot open master key directory: %w", ErrMasterKeyState)
	}
	defer func() { _ = root.Close() }()
	if key, err = readMasterKey(root); err == nil {
		return key, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	key = make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, errors.New("failed to generate master key")
	}
	var nonce [16]byte
	if _, err := io.ReadFull(rand.Reader, nonce[:]); err != nil {
		return nil, errors.New("failed to generate master key staging name")
	}
	name := ".secret-key-" + hex.EncodeToString(nonce[:])
	file, err := rootfile.Open(root, name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, fmt.Errorf("cannot stage master key: %w", ErrMasterKeyState)
	}
	defer func() {
		if removeErr := root.Remove(name); removeErr != nil {
			key = nil
			err = errors.Join(err, fmt.Errorf("cannot remove master key staging file: %w", ErrMasterKeyState))
		} else if syncErr := syncMasterKeyDirectory(root); syncErr != nil {
			key = nil
			err = errors.Join(err, syncErr)
		}
	}()
	_, writeErr := file.Write(key)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return nil, fmt.Errorf("cannot persist master key: %w", ErrMasterKeyState)
	}
	if err := root.Link(name, "secret.key"); err != nil {
		if errors.Is(err, os.ErrExist) {
			return readMasterKey(root)
		}
		return nil, fmt.Errorf("cannot publish master key: %w", ErrMasterKeyState)
	}
	return readMasterKey(root)
}

func readMasterKey(root *os.Root) ([]byte, error) {
	// NONBLOCK prevents an unexpected FIFO from blocking startup before Stat.
	file, err := rootfile.Open(root, "secret.key", os.O_RDONLY, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, os.ErrNotExist
		}
		return nil, ErrMasterKeyState
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || info.Size() != 32 {
		return nil, ErrMasterKeyState
	}
	data, err := io.ReadAll(io.LimitReader(file, 33))
	if err != nil || len(data) != 32 {
		return nil, ErrMasterKeyState
	}
	// Every caller syncs the published name, including concurrent readers.
	if err := syncMasterKeyDirectory(root); err != nil {
		return nil, err
	}
	return data, nil
}
func syncMasterKeyDirectory(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("cannot sync master key directory: %w", ErrMasterKeyState)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil || closeErr != nil {
		return fmt.Errorf("cannot sync master key directory: %w", ErrMasterKeyState)
	}
	return nil
}
