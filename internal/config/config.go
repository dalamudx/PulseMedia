package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
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
type OrganizationConfig struct {
	MinMediaBytes int64 `yaml:"min_media_bytes"`
}

type Config struct {
	Server          ServerConfig          `yaml:"server"`
	Database        DatabaseConfig        `yaml:"database"`
	Organization    OrganizationConfig    `yaml:"organization"`
	DownloadRouting DownloadRoutingConfig `yaml:"download_routing"`

	WebPush             WebPushConfig             `yaml:"web_push"`
	DownloadGlobalRates DownloadGlobalRatesConfig `yaml:"download_global_rates"`
	DataDir             string                    `yaml:"data_dir"`
	LogLevel            string                    `yaml:"log_level"`
	SecretKey           string                    `yaml:"secret_key"`
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
		Organization: OrganizationConfig{MinMediaBytes: 1},
		DataDir:      ".data",
		LogLevel:     "info",
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
	if err := l.loadWebPush(cfg); err != nil {
		return nil, err
	}
	if raw, ok := l.envLookup(l.envPrefix + "DOWNLOAD_GLOBAL_RATES_ENABLED"); ok {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid download global rates switch", ErrInvalidConfig)
		}
		cfg.DownloadGlobalRates.Enabled = enabled
	}
	if raw, ok := l.envLookup(l.envPrefix + "DOWNLOAD_ROUTING_ENABLED"); ok {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, routingConfigError()
		}
		cfg.DownloadRouting.Enabled = &enabled
	}
	if raw, ok := l.envLookup(l.envPrefix + "DOWNLOAD_ROUTE_RULES"); ok {
		rules, err := decodeDownloadRouteRules(raw)
		if err != nil {
			return nil, err
		}
		cfg.DownloadRouting.Rules = rules
	}
	if val, ok := l.envLookup(l.envPrefix + "ORGANIZATION_MIN_MEDIA_BYTES"); ok {
		minimum, err := strconv.ParseInt(val, 10, 64)
		if err != nil || minimum < 1 {
			return nil, fmt.Errorf("%w: invalid organization minimum media bytes", ErrInvalidConfig)
		}
		cfg.Organization.MinMediaBytes = minimum
	}
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
	if err := c.WebPush.validate(); err != nil {
		return err
	}
	if err := c.DownloadRouting.validate(); err != nil {
		return err
	}
	if c.Organization.MinMediaBytes < 1 {
		return fmt.Errorf("%w: organization.min_media_bytes must be positive", ErrInvalidConfig)
	}
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
