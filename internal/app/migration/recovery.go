package migration

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"filippo.io/age"
	"github.com/dalamudx/PulseMedia/internal/app/backup"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfile"
	"github.com/google/uuid"
)

// Matches the versioned archive contract produced by backup.Export. The private
// dump is hashed directly from the authenticated stream; no plaintext spool is
// created by the migration tool. This checks recovery material, not a restore
// rehearsal or provenance/freshness beyond schema and the original master key.
type manifest struct {
	ID                uuid.UUID
	Schema            int64
	DumpHash, KeyHash string
	Dump, Key         []byte
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(b)
}
func hash(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }

func validateRecovery(ctx context.Context, o Options, schema int64) error {
	key, err := backup.ReadPrivate(filepath.Join(o.DataDir, "secret.key"))
	if err != nil || len(key) != 32 {
		return ErrRecovery
	}
	identity, err := backup.ReadPrivate(o.IdentityFile)
	if err != nil {
		return ErrRecovery
	}
	ids, err := age.ParseIdentities(strings.NewReader(string(identity)))
	if err != nil || len(ids) == 0 {
		return ErrRecovery
	}
	for _, id := range ids {
		if _, ok := id.(*age.X25519Identity); !ok {
			return ErrRecovery
		}
	}
	database, err := inspectArchive(ctx, o.Archive, ids, true)
	if err != nil {
		return ErrRecovery
	}
	master, err := inspectArchive(ctx, o.KeyArchive, ids, false)
	if err != nil {
		return ErrRecovery
	}
	if database.ID == uuid.Nil || database.ID != master.ID || database.Schema != schema || master.Schema != schema ||
		database.KeyHash != hash(key) || master.KeyHash != database.KeyHash || master.DumpHash != database.DumpHash ||
		!bytes.Equal(master.Key, key) {
		return ErrRecovery
	}
	return nil
}

func inspectArchive(ctx context.Context, path string, ids []age.Identity, database bool) (m manifest, err error) {
	root, err := rootfile.OpenDirectory(filepath.Dir(path), false)
	if err != nil {
		return m, ErrRecovery
	}
	defer func() { _ = root.Close() }()
	f, err := rootfile.Open(root, filepath.Base(path), os.O_RDONLY, 0)
	if err != nil {
		return m, ErrRecovery
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0600 || st.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return m, ErrRecovery
	}
	reader, err := age.Decrypt(contextReader{ctx, f}, ids...)
	if err != nil {
		return m, ErrRecovery
	}
	tr := tar.NewReader(contextReader{ctx, reader})
	h, err := tr.Next()
	if err != nil || h.Name != "manifest.json" || h.Typeflag != tar.TypeReg || h.Size < 1 || h.Size > 1<<20 {
		return m, ErrRecovery
	}
	dec := json.NewDecoder(tr)
	dec.DisallowUnknownFields()
	if err = dec.Decode(&m); err != nil {
		return m, ErrRecovery
	}
	var extra any
	if dec.Decode(&extra) != io.EOF || len(m.Dump) != 0 || m.ID == uuid.Nil || m.Schema < 1 {
		return m, ErrRecovery
	}
	for _, s := range []string{m.DumpHash, m.KeyHash} {
		decoded, e := hex.DecodeString(s)
		if e != nil || len(decoded) != sha256.Size {
			return m, ErrRecovery
		}
	}
	if database {
		if len(m.Key) != 0 {
			return m, ErrRecovery
		}
		h, err = tr.Next()
		if err != nil || h.Name != "database.dump" || h.Typeflag != tar.TypeReg || h.Size < 5 || h.Size > 1<<40 {
			return m, ErrRecovery
		}
		digest := sha256.New()
		var magic [5]byte
		if _, err = io.ReadFull(tr, magic[:]); err != nil || string(magic[:]) != "PGDMP" {
			return m, ErrRecovery
		}
		_, _ = digest.Write(magic[:])
		// Parse and decompress the complete custom dump with the same-major
		// production client, without connecting to any database or executing SQL.
		// A digest/magic-only check would accept an unreadable recovery payload.
		version, e := exec.CommandContext(ctx, "pg_restore", "--version").Output()
		if e != nil || !strings.HasPrefix(string(version), "pg_restore (PostgreSQL) 18.") {
			return m, ErrRecovery
		}
		restore := exec.CommandContext(ctx, "pg_restore", "--file=-", "--no-owner", "--no-privileges")
		// The first bytes have already entered the digest.
		restore.Stdin = io.MultiReader(bytes.NewReader(magic[:]), io.TeeReader(tr, digest))
		restore.Stdout = io.Discard
		restore.Env = []string{"PATH=" + os.Getenv("PATH"), "TMPDIR=" + os.Getenv("TMPDIR"), "TMP=" + os.Getenv("TMP"), "TEMP=" + os.Getenv("TEMP")}
		if err = restore.Run(); err != nil {
			return m, ErrRecovery
		}
		// pg_restore may finish after its last data block without consuming the
		// custom-format trailer. Hash the remaining tar entry before comparing.
		if _, err = io.Copy(digest, tr); err != nil || hex.EncodeToString(digest.Sum(nil)) != m.DumpHash {
			return m, ErrRecovery
		}
	} else if len(m.Key) != 32 || hash(m.Key) != m.KeyHash {
		return m, ErrRecovery
	}
	if _, err = tr.Next(); !errors.Is(err, io.EOF) {
		return m, ErrRecovery
	}
	// tar EOF occurs before age's authenticated EOF. Consume the entire age stream
	// so a truncated/corrupted final chunk cannot authorize DDL.
	if _, err = io.Copy(io.Discard, contextReader{ctx, reader}); err != nil {
		return m, ErrRecovery
	}
	return m, ctx.Err()
}
