package migration

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"testing"

	"filippo.io/age"
)

// Modify the independently produced backup.Export fixture, retaining a fully
// authenticated age envelope. This distinguishes payload/binding validation
// from simply checking that age decryption succeeds.
func rewriteArchive(t *testing.T, encrypted []byte, identity *age.X25519Identity, mutate func(map[string]any, *[]byte)) []byte {
	t.Helper()
	reader, err := age.Decrypt(bytes.NewReader(encrypted), identity)
	if err != nil {
		t.Fatal("fixture decryption failed")
	}
	tr := tar.NewReader(reader)
	h, err := tr.Next()
	if err != nil || h.Name != "manifest.json" {
		t.Fatal("fixture manifest missing")
	}
	var fields map[string]any
	if err = json.NewDecoder(tr).Decode(&fields); err != nil {
		t.Fatal("fixture manifest invalid")
	}
	var dump []byte
	if h, err = tr.Next(); err == nil {
		if h.Name != "database.dump" {
			t.Fatal("fixture dump missing")
		}
		dump, err = io.ReadAll(tr)
		if err != nil {
			t.Fatal("fixture dump unreadable")
		}
	}
	mutate(fields, &dump)
	var out bytes.Buffer
	sealed, err := age.Encrypt(&out, identity.Recipient())
	if err != nil {
		t.Fatal("fixture encryption failed")
	}
	tw := tar.NewWriter(sealed)
	manifest, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err = tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0600, Size: int64(len(manifest))}); err != nil {
		t.Fatal(err)
	}
	if _, err = tw.Write(manifest); err != nil {
		t.Fatal(err)
	}
	if dump != nil {
		if err = tw.WriteHeader(&tar.Header{Name: "database.dump", Mode: 0600, Size: int64(len(dump))}); err != nil {
			t.Fatal(err)
		}
		if _, err = tw.Write(dump); err != nil {
			t.Fatal(err)
		}
	}
	if err = tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err = sealed.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
func independentDigest(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
