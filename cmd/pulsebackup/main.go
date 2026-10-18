package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/dalamudx/PulseMedia/internal/app/backup"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
)

func main() {
	if err := run(); err != nil {
		if errors.Is(err, backup.ErrCleanup) {
			_, _ = fmt.Fprintln(os.Stderr, err)
		} else {
			_, _ = fmt.Fprintln(os.Stderr, "pulsebackup: operation failed; verify private inputs, target and offline state")
		}
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("export, restore, status or reconcile required")
	}
	f := flag.NewFlagSet(os.Args[1], flag.ContinueOnError)
	f.SetOutput(io.Discard)
	var o backup.Options
	var evidence string
	f.StringVar(&o.DataDir, "data-dir", "", "offline application data directory")
	f.StringVar(&o.ConnectionFile, "connection-file", "", "0600 explicit PostgreSQL connection string file")
	f.StringVar(&o.Archive, "archive", "", "database age archive path")
	f.StringVar(&o.KeyArchive, "key-archive", "", "separate master key age archive path")
	f.StringVar(&o.Recipient, "recipient", "", "public age X25519 recipient")
	f.StringVar(&o.IdentityFile, "identity-file", "", "0600 independent age identity file")
	f.StringVar(&o.Target, "target", "", "new pulse_restore_<name> database")
	f.StringVar(&o.Migrations, "migrations", "", "migration directory")
	f.StringVar(&evidence, "evidence-file", "", "JSON RestoreReconciliation evidence file; exclude credentials and URLs")
	if len(os.Args) == 3 && (os.Args[2] == "--help" || os.Args[2] == "-h") {
		f.SetOutput(os.Stdout)
		f.PrintDefaults()
		return nil
	}
	if e := f.Parse(os.Args[2:]); e != nil {
		return e
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch os.Args[1] {
	case "export":
		return backup.Export(ctx, o)
	case "restore":
		return backup.Restore(ctx, o)
	case "status":
		s, e := backup.Inspect(ctx, o)
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(s)
	case "reconcile":
		b, e := backup.ReadPrivate(evidence)
		if e != nil {
			return e
		}
		var r postgres.RestoreReconciliation
		decoder := json.NewDecoder(bytes.NewReader(b))
		decoder.DisallowUnknownFields()
		if e = decoder.Decode(&r); e != nil {
			return e
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			return errors.New("trailing evidence")
		}
		return backup.Reconcile(ctx, o, r)
	}
	return fmt.Errorf("unknown command")
}
