package backup

import (
	"bytes"
	"filippo.io/age"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestLargeArchiveBoundedMemory(t *testing.T) {
	dir, e := os.MkdirTemp("/data/temp", "pulse-backup-large-")
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		if e := os.RemoveAll(dir); e != nil {
			t.Error(e)
		}
		if _, e := os.Stat(dir); !os.IsNotExist(e) {
			t.Error("scratch remains")
		}
	}()
	f, e := os.OpenFile(filepath.Join(dir, "dump"), os.O_CREATE|os.O_RDWR|os.O_EXCL, 0600)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = f.Close() }()
	if e = f.Truncate(520 << 20); e != nil {
		t.Fatal(e)
	}
	hash, e := fileDigest(f)
	if e != nil {
		t.Fatal(e)
	}
	id, e := age.GenerateX25519Identity()
	if e != nil {
		t.Fatal(e)
	}
	identity := filepath.Join(dir, "identity")
	if e = os.WriteFile(identity, []byte(id.String()), 0600); e != nil {
		t.Fatal(e)
	}
	archive, e := os.OpenFile(filepath.Join(dir, "dump.age"), os.O_CREATE|os.O_RDWR|os.O_EXCL, 0600)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = archive.Close() }()
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	stop := make(chan struct{})
	done := make(chan uint64, 1)
	go func() {
		ticker := time.NewTicker(2 * time.Millisecond)
		defer ticker.Stop()
		peak := before.HeapAlloc
		for {
			select {
			case <-ticker.C:
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				if m.HeapAlloc > peak {
					peak = m.HeapAlloc
				}
			case <-stop:
				done <- peak
				return
			}
		}
	}()
	defer func() {
		close(stop)
		peak := <-done
		t.Logf("520 MiB roundtrip peak heap above baseline %.2f MiB", float64(peak-before.HeapAlloc)/(1<<20))
		if peak-before.HeapAlloc > 64<<20 {
			t.Error("archive heap exceeded 64 MiB")
		}
	}()
	if e = sealStream(archive, envelope{ID: uuid.New(), Schema: 83, DumpHash: hash, KeyHash: digest(bytes.Repeat([]byte{1}, 32)), Spool: f.Name()}, id.Recipient().String()); e != nil {
		t.Fatal(e)
	}
	got, e := unseal(archive.Name(), identity)
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		if e := os.Remove(got.Spool); e != nil {
			t.Error(e)
		}
	}()
	restored, e := os.Open(got.Spool)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = restored.Close() }()
	actual, e := fileDigest(restored)
	if e != nil || actual != hash {
		t.Fatal("streamed dump changed", e)
	}
}
