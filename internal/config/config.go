// Package config loads slskdpp settings from a YAML file with SLSKDPP_*
// environment variable overrides.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Soulseek    Soulseek    `yaml:"soulseek"`
	Directories Directories `yaml:"directories"`
	Web         Web         `yaml:"web"`
	Transfers   Transfers   `yaml:"transfers"`
	Hooks       Hooks       `yaml:"hooks"`
	Wishlist    Wishlist    `yaml:"wishlist"`
	Metrics     Metrics     `yaml:"metrics"`
	Shares      []string    `yaml:"shares"`
	LogLevel    string      `yaml:"log_level"`
	// LogFiles writes the log to daily files in <data>/logs too, kept for
	// LogKeepDays days.
	LogFiles    bool `yaml:"log_files"`
	LogKeepDays int  `yaml:"log_keep_days"`

	// Path is the config file, whether or not it existed when loaded.
	Path string `yaml:"-"`
}

// LogDir is where log files go.
func (c Config) LogDir() string { return filepath.Join(c.Directories.Data, "logs") }

type Soulseek struct {
	Username   string `yaml:"username"`
	Password   string `yaml:"password"`
	Server     string `yaml:"server"`
	ListenPort uint32 `yaml:"listen_port"`
}

type Directories struct {
	Data       string `yaml:"data"`
	Downloads  string `yaml:"downloads"`
	Incomplete string `yaml:"incomplete"`
}

type Transfers struct {
	// MaxAttempts is how often a source is tried before another is sought.
	MaxAttempts int `yaml:"max_attempts"`
	// MaxSources caps how many users a file is tried from.
	MaxSources int `yaml:"max_sources"`
	// Speed limits in KiB/s; 0 means unlimited.
	DownloadSpeedLimit    int64 `yaml:"download_speed_limit"`
	PerDownloadSpeedLimit int64 `yaml:"per_download_speed_limit"`
	UploadSpeedLimit      int64 `yaml:"upload_speed_limit"`
	// UploadSlots is how many uploads run at once.
	UploadSlots int `yaml:"upload_slots"`
	// MaxQueuedPerUser caps how many files one user may queue from us.
	MaxQueuedPerUser int `yaml:"max_queued_per_user"`
}

type Wishlist struct {
	// Interval is the minutes between wishlist searches; the server's
	// interval is the minimum. 0 uses the server's.
	Interval int `yaml:"interval"`
}

// Hooks tell other programs about finished downloads.
type Hooks struct {
	// OnDownload and OnFolder are shell commands run after each file and
	// after each folder; details are in SLSKDPP_* environment variables.
	OnDownload string `yaml:"on_download"`
	OnFolder   string `yaml:"on_folder"`
	// Webhook receives the same events as JSON POSTs.
	Webhook string `yaml:"webhook"`
}

type Web struct {
	Listen   string `yaml:"listen"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	// URLBase serves the UI and API under this path, such as /slskd
	// behind a reverse proxy, as slskd's web.url_base does. Paths without
	// it are served too.
	URLBase string `yaml:"url_base"`
	// APIKeys are accepted in X-API-Key or Authorization: Bearer headers,
	// besides keys created in the web UI.
	APIKeys []string `yaml:"api_keys"`
	// TrustedProxies are reverse proxies (IPs or CIDR ranges) whose
	// X-Forwarded-For header names the real client. Loopback is trusted
	// when this is empty.
	TrustedProxies []string `yaml:"trusted_proxies"`
	// RemoteFileManagement lets the slskd-compatible API delete files and
	// folders in the downloads and incomplete directories. Off by
	// default, as in slskd.
	RemoteFileManagement bool `yaml:"remote_file_management"`
	// RemoteConfiguration lets the slskd-compatible API read and rewrite
	// the config file and change the listen port. Off by default, as in
	// slskd.
	RemoteConfiguration bool `yaml:"remote_configuration"`
}

// Metrics serves Prometheus metrics, as slskd's metrics options do.
type Metrics struct {
	Enabled bool   `yaml:"enabled"`
	Path    string `yaml:"path"`
	// Username and Password, if both set, protect the metrics with basic
	// auth; otherwise they need an API key or a login.
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

func Default() Config {
	return Config{
		Soulseek: Soulseek{
			Server:     "server.slsknet.org:2242",
			ListenPort: 2234,
		},
		Directories: Directories{Data: "data"},
		Transfers:   Transfers{MaxAttempts: 5, MaxSources: 3, UploadSlots: 2, MaxQueuedPerUser: 500},
		Web:         Web{Listen: ":5030", Username: "slskdpp"},
		Metrics:     Metrics{Path: "/metrics"},
		LogLevel:    "info",
		LogFiles:    true,
		LogKeepDays: 7,
	}
}

// Parse reads a config file's contents, refusing unknown keys, without
// environment overrides or derived defaults. It is for checking a file
// before writing it.
func Parse(b []byte) (Config, error) {
	cfg := Default()
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return cfg, err
	}
	return cfg, nil
}

// Load reads path (if it exists), applies environment overrides and
// fills derived defaults. A missing file is not an error.
func Load(path string) (Config, error) {
	cfg := Default()
	cfg.Path = path
	if path != "" {
		b, err := os.ReadFile(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
		case err != nil:
			return cfg, err
		default:
			if err := yaml.Unmarshal(b, &cfg); err != nil {
				return cfg, fmt.Errorf("parse %s: %w", path, err)
			}
		}
	}
	if err := cfg.ApplyEnv(); err != nil {
		return cfg, err
	}
	cfg.Web.URLBase = CleanURLBase(cfg.Web.URLBase)
	if cfg.Directories.Downloads == "" {
		cfg.Directories.Downloads = filepath.Join(cfg.Directories.Data, "downloads")
	}
	if cfg.Directories.Incomplete == "" {
		cfg.Directories.Incomplete = filepath.Join(cfg.Directories.Data, "incomplete")
	}
	return cfg, nil
}

// ApplyEnv applies SLSKDPP_* environment overrides.
func (cfg *Config) ApplyEnv() error {
	strs := map[string]*string{
		"SLSKDPP_USERNAME":         &cfg.Soulseek.Username,
		"SLSKDPP_PASSWORD":         &cfg.Soulseek.Password,
		"SLSKDPP_SERVER":           &cfg.Soulseek.Server,
		"SLSKDPP_DATA_DIR":         &cfg.Directories.Data,
		"SLSKDPP_DOWNLOADS_DIR":    &cfg.Directories.Downloads,
		"SLSKDPP_INCOMPLETE_DIR":   &cfg.Directories.Incomplete,
		"SLSKDPP_WEB_LISTEN":       &cfg.Web.Listen,
		"SLSKDPP_WEB_USERNAME":     &cfg.Web.Username,
		"SLSKDPP_WEB_PASSWORD":     &cfg.Web.Password,
		"SLSKDPP_WEB_URL_BASE":     &cfg.Web.URLBase,
		"SLSKDPP_METRICS_USERNAME": &cfg.Metrics.Username,
		"SLSKDPP_METRICS_PASSWORD": &cfg.Metrics.Password,
		"SLSKDPP_LOG_LEVEL":        &cfg.LogLevel,
	}
	for key, dst := range strs {
		if v, ok := os.LookupEnv(key); ok {
			*dst = v
		}
	}
	if v, ok := os.LookupEnv("SLSKDPP_API_KEY"); ok && v != "" {
		cfg.Web.APIKeys = append(cfg.Web.APIKeys, v)
	}
	if v, ok := os.LookupEnv("SLSKDPP_WEB_TRUSTED_PROXIES"); ok {
		cfg.Web.TrustedProxies = strings.Split(v, ",")
	}
	if v, ok := os.LookupEnv("SLSKDPP_LISTEN_PORT"); ok {
		port, err := strconv.ParseUint(v, 10, 16)
		if err != nil {
			return fmt.Errorf("SLSKDPP_LISTEN_PORT: %w", err)
		}
		cfg.Soulseek.ListenPort = uint32(port)
	}
	if v, ok := os.LookupEnv("SLSKDPP_METRICS_ENABLED"); ok {
		on, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("SLSKDPP_METRICS_ENABLED: %w", err)
		}
		cfg.Metrics.Enabled = on
	}
	return nil
}

// CleanURLBase gives a URL base one leading slash and no trailing one;
// "" and "/" mean none, which is "".
func CleanURLBase(base string) string {
	base = strings.Trim(strings.TrimSpace(base), "/")
	if base == "" {
		return ""
	}
	return "/" + base
}

// Validate reports settings that prevent connecting to the network.
func (c Config) Validate() error {
	var errs []error
	if c.Soulseek.Username == "" {
		errs = append(errs, errors.New("soulseek.username is required"))
	}
	if c.Soulseek.Password == "" {
		errs = append(errs, errors.New("soulseek.password is required"))
	}
	if c.Soulseek.ListenPort == 0 || c.Soulseek.ListenPort > 65535 {
		errs = append(errs, errors.New("soulseek.listen_port must be 1-65535"))
	}
	if strings.ContainsAny(c.Web.URLBase, "?#\\ ") || slices.Contains(strings.Split(c.Web.URLBase, "/"), "..") {
		errs = append(errs, errors.New("web.url_base must be a plain path such as /slskd"))
	}
	if c.Metrics.Enabled && (!strings.HasPrefix(c.Metrics.Path, "/") || strings.ContainsAny(c.Metrics.Path, "{}?# \t")) {
		errs = append(errs, errors.New("metrics.path must start with /"))
	}
	if c.LogKeepDays < 0 {
		errs = append(errs, errors.New("log_keep_days must not be negative"))
	}
	return errors.Join(errs...)
}
