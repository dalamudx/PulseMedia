package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/config"
)

// Version and Commit are set during build via ldflags.
var (
	Version = "0.1.0-beta"
	Commit  = "none"
	Date    = "unknown"
)

func main() {
	var (
		versionFlag = flag.Bool("v", false, "print version information and exit")
		configPath  = flag.String("c", "", "path to YAML configuration file")
		dataDir     = flag.String("data-dir", "", "path to application data directory")
		httpAddr    = flag.String("http-addr", "", "HTTP listen address (e.g. 127.0.0.1:8080)")
		logLevel    = flag.String("log-level", "", "log level (debug, info, warn, error)")
	)
	flag.BoolVar(versionFlag, "version", false, "print version information and exit")
	flag.StringVar(configPath, "config", "", "path to YAML configuration file")
	flag.Parse()

	if *versionFlag {
		fmt.Printf("PulseMedia %s (commit: %s, built: %s)\n", Version, Commit, Date)
		return
	}

	opts := make([]config.Option, 0)
	if *configPath != "" {
		opts = append(opts, config.WithConfigFile(*configPath))
	}
	if *dataDir != "" {
		opts = append(opts, config.WithDataDir(*dataDir))
	}
	if *httpAddr != "" {
		opts = append(opts, config.WithHTTPAddr(*httpAddr))
	}
	if *logLevel != "" {
		opts = append(opts, config.WithLogLevel(*logLevel))
	}

	cfg, err := config.Load(opts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Configuration error: %v\n", err)
		os.Exit(1)
	}

	application, err := app.New(cfg, app.WithVersion(Version))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Initialization error: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		_ = application.Close()
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := application.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Application runtime error: %v\n", err)
		os.Exit(1)
	}
}
