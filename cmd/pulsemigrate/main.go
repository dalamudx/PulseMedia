package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/dalamudx/PulseMedia/internal/app/migration"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		// Print only closed, local error messages. Neither pgx/goose errors, paths nor
		// rejected arguments may reach stderr: any could contain raw credentials.
		_, _ = fmt.Fprintln(os.Stderr, "pulsemigrate:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	f := flag.NewFlagSet("pulsemigrate", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	var o migration.Options
	f.StringVar(&o.DataDir, "data-dir", "", "required offline application data directory (same lock as app/backup)")
	f.StringVar(&o.ConnectionFile, "connection-file", "", "required regular 0600 file <=1 MiB containing an explicit PostgreSQL URI; uses explicit settings only; unset PGSERVICE; ignores home defaults")
	f.StringVar(&o.Migrations, "migrations", "", "required SQL migration directory (current binary schema)")
	f.StringVar(&o.Archive, "archive", "", "existing database: encrypted database archive from pulsebackup export")
	f.StringVar(&o.KeyArchive, "key-archive", "", "existing database: separate encrypted master-key archive from the same export")
	f.StringVar(&o.IdentityFile, "identity-file", "", "existing database: regular 0600 age X25519 identity file")
	f.Usage = func() {
		_, _ = fmt.Fprintln(out, "Usage: pulsemigrate --data-dir DIR --connection-file FILE --migrations DIR [--archive FILE --key-archive FILE --identity-file FILE]")
		_, _ = fmt.Fprintln(out, "Offline PostgreSQL 18 install/upgrade. Stop application and backup tools first; use the original data directory.")
		_, _ = fmt.Fprintln(out, "An existing empty database with no user objects may be installed without archives; no master key is generated.")
		_, _ = fmt.Fprintln(out, "Existing schemas require authenticated matching recovery material and the original secret.key, including repeat invocations.")
		_, _ = fmt.Fprintln(out, "Connection URI must explicitly name host, user and database with sslmode=verify-full; sslmode=disable is allowed only for local development.")
		_, _ = fmt.Fprintln(out, "Existing-schema archive validation also requires pg_restore 18 in PATH; it decodes the dump without connecting to a database.")
		_, _ = fmt.Fprintln(out, "Schema/key/dump authentication checks do not establish archive freshness or replace a restore rehearsal; select a current export from this database.")
		f.SetOutput(out)
		f.PrintDefaults()
	}
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return migration.ErrInput
	}
	if f.NArg() != 0 {
		return migration.ErrInput
	}
	r, err := migration.Run(ctx, o)
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintf(out, "migration completed: schema %d -> %d\n", r.PreviousVersion, r.Version); err != nil {
		return errors.New("migration completed but result output failed; inspect database before retrying")
	}
	return nil
}
