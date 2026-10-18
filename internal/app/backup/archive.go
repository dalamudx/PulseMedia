package backup

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"filippo.io/age"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfile"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func spool() (*os.File, error) { return os.CreateTemp("/data/temp", "pulse-backup-spool-") }
func fileDigest(f *os.File) (string, error) {
	if _, e := f.Seek(0, io.SeekStart); e != nil {
		return "", e
	}
	h := sha256.New()
	if _, e := io.Copy(h, f); e != nil {
		return "", e
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func seal(v envelope, recipient string) ([]byte, error) {
	var b bytes.Buffer
	e := sealStream(&b, v, recipient)
	return b.Bytes(), e
}
func sealStream(out io.Writer, v envelope, recipient string) error {
	r, e := age.ParseX25519Recipient(recipient)
	if e != nil {
		return e
	}
	w, e := age.Encrypt(out, r)
	if e != nil {
		return e
	}
	tw := tar.NewWriter(w)
	manifest, e := json.Marshal(v)
	if e != nil {
		return e
	}
	if e = tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0600, Size: int64(len(manifest))}); e != nil {
		return e
	}
	if _, e = tw.Write(manifest); e != nil {
		return e
	}
	if v.Spool != "" {
		f, e := os.Open(v.Spool)
		if e != nil {
			return e
		}
		defer func() { _ = f.Close() }()
		st, e := f.Stat()
		if e != nil {
			return e
		}
		if e = tw.WriteHeader(&tar.Header{Name: "database.dump", Mode: 0600, Size: st.Size()}); e != nil {
			return e
		}
		if _, e = io.Copy(tw, f); e != nil {
			return e
		}
	}
	if e = tw.Close(); e != nil {
		return e
	}
	return w.Close()
}
func unseal(path, identity string) (v envelope, err error) {
	b, e := private(identity)
	if e != nil {
		return v, e
	}
	ids, e := age.ParseIdentities(strings.NewReader(string(b)))
	if e != nil {
		return v, e
	}
	root, e := rootfile.OpenDirectory(filepath.Dir(path), false)
	if e != nil {
		return v, e
	}
	defer func() { _ = root.Close() }()
	f, e := rootfile.Open(root, filepath.Base(path), os.O_RDONLY, 0)
	if e != nil {
		return v, e
	}
	defer func() { _ = f.Close() }()
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0600 {
		return v, errors.New("private archive required")
	}
	reader, e := age.Decrypt(f, ids...)
	if e != nil {
		return v, e
	}
	tr := tar.NewReader(reader)
	h, e := tr.Next()
	if e != nil || h.Name != "manifest.json" || h.Size > 1<<20 || h.Typeflag != tar.TypeReg {
		return v, errors.New("invalid manifest")
	}
	dec := json.NewDecoder(tr)
	dec.DisallowUnknownFields()
	if e = dec.Decode(&v); e != nil {
		return v, e
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return v, errors.New("trailing manifest")
	}
	h, e = tr.Next()
	if e == nil {
		if h.Name != "database.dump" || h.Typeflag != tar.TypeReg {
			return v, errors.New("invalid dump entry")
		}
		dump, e := spool()
		if e != nil {
			return v, e
		}
		v.Spool = dump.Name()
		defer func() {
			if err != nil {
				_ = os.Remove(v.Spool)
			}
		}()
		_, e = io.Copy(dump, tr)
		ce := dump.Close()
		if e != nil {
			return v, e
		}
		if ce != nil {
			return v, ce
		}
		if _, e = tr.Next(); e != io.EOF {
			return v, errors.New("extra archive entry")
		}
	} else if e != io.EOF {
		return v, e
	}
	// Consume the age authentication trailer after tar's end markers.
	if _, e = io.Copy(io.Discard, reader); e != nil {
		return v, e
	}
	return v, nil
}
