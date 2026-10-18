package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var (
	ErrInvalidConfig = errors.New("invalid configuration")
)

type ServerConfig struct {
	HTTPAddr     string        `yaml:"http_addr"`
	ReadTimeout  time.Duration `yaml:"read_timeout"`
	WriteTimeout time.Duration `yaml:"write_timeout"`
}

type DatabaseConfig struct {
	URL string `yaml:"url"`
}

type Config struct {
	Server    ServerConfig   `yaml:"server"`
	Database  DatabaseConfig `yaml:"database"`
	DataDir   string         `yaml:"data_dir"`
	LogLevel  string         `yaml:"log_level"`
	SecretKey string         `yaml:"secret_key"`
}

// Option configures how options are loaded.
type Option func(*loader)

type loader struct {
	configFilePath string
	envPrefix      string
	envLookup      func(string) (string, bool)
	dataDir        string
	httpAddr       string
	logLevel       string
	dbURL          string
}

func WithConfigFile(path string) Option {
	return func(l *loader) {
		l.configFilePath = path
	}
}

func WithEnvLookup(lookup func(string) (string, bool)) Option {
	return func(l *loader) {
		l.envLookup = lookup
	}
}

func WithDataDir(dir string) Option {
	return func(l *loader) {
		l.dataDir = dir
	}
}

func WithHTTPAddr(addr string) Option {
	return func(l *loader) {
		l.httpAddr = addr
	}
}

func WithLogLevel(lvl string) Option {
	return func(l *loader) {
		l.logLevel = lvl
	}
}

func WithDatabaseURL(url string) Option {
	return func(l *loader) {
		l.dbURL = url
	}
}

// NewDefault returns baseline default configuration.
func NewDefault() *Config {
	return &Config{
		Server: ServerConfig{
			HTTPAddr:     "127.0.0.1:8080",
			ReadTimeout:  15 * time.Second,
			WriteTimeout: 30 * time.Second,
		},
		Database: DatabaseConfig{
			URL: "",
		},
		DataDir:  ".data",
		LogLevel: "info",
	}
}

// Load loads configuration observing precedence: Defaults -> File -> Env -> Explicit Options.
func Load(opts ...Option) (*Config, error) {
	cfg := NewDefault()
	l := &loader{
		envPrefix: "PULSE_",
		envLookup: os.LookupEnv,
	}
	for _, opt := range opts {
		opt(l)
	}

	// 1. File loading if configured or default config.yaml exists
	filePath := l.configFilePath
	if filePath == "" {
		if _, err := os.Stat("config.yaml"); err == nil {
			filePath = "config.yaml"
		}
	}

	if filePath != "" {
		data, err := os.ReadFile(filePath)
		if err != nil {
			return nil, fmt.Errorf("failed to read config file %s: %w", filePath, err)
		}
		if err := yaml.Unmarshal(data, cfg); err != nil {
			// YAML type errors can contain configuration scalar values.
			return nil, fmt.Errorf("%w: failed to parse YAML in %s", ErrInvalidConfig, filePath)
		}
	}

	// 2. Environment variables override
	if val, ok := l.envLookup(l.envPrefix + "HTTP_ADDR"); ok && val != "" {
		cfg.Server.HTTPAddr = val
	}
	if val, ok := l.envLookup(l.envPrefix + "DATABASE_URL"); ok && val != "" {
		cfg.Database.URL = val
	}
	if val, ok := l.envLookup(l.envPrefix + "DATA_DIR"); ok && val != "" {
		cfg.DataDir = val
	}
	if val, ok := l.envLookup(l.envPrefix + "LOG_LEVEL"); ok && val != "" {
		cfg.LogLevel = val
	}
	if val, ok := l.envLookup(l.envPrefix + "SECRET_KEY"); ok && val != "" {
		cfg.SecretKey = val
	}

	// 3. Explicit options override
	if l.httpAddr != "" {
		cfg.Server.HTTPAddr = l.httpAddr
	}
	if l.dataDir != "" {
		cfg.DataDir = l.dataDir
	}
	if l.logLevel != "" {
		cfg.LogLevel = l.logLevel
	}
	if l.dbURL != "" {
		cfg.Database.URL = l.dbURL
	}

	// Validate the original value before Clean can turn emptiness into ".".
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg.DataDir = filepath.Clean(cfg.DataDir)

	return cfg, nil
}

// Validate checks all configuration constraints and invariants.
func (c *Config) Validate() error {
	if strings.TrimSpace(c.DataDir) == "" {
		return fmt.Errorf("%w: data_dir cannot be empty", ErrInvalidConfig)
	}

	if c.Server.HTTPAddr == "" {
		return fmt.Errorf("%w: server.http_addr cannot be empty", ErrInvalidConfig)
	}
	host, port, err := net.SplitHostPort(c.Server.HTTPAddr)
	if err != nil {
		return fmt.Errorf("%w: invalid server.http_addr (must be host:port)", ErrInvalidConfig)
	}
	_ = host
	if port == "" {
		return fmt.Errorf("%w: missing port in server.http_addr", ErrInvalidConfig)
	}

	validLogLevels := map[string]bool{"debug": true, "info": true, "warn": true, "warning": true, "error": true}
	if !validLogLevels[strings.ToLower(c.LogLevel)] {
		return fmt.Errorf("%w: invalid log_level (must be debug, info, warn, or error)", ErrInvalidConfig)
	}

	if c.Database.URL != "" {
		u, err := url.Parse(c.Database.URL)
		if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
			return fmt.Errorf("%w: database.url must be a valid postgres:// or postgresql:// URL", ErrInvalidConfig)
		}
	}

	return nil
}
