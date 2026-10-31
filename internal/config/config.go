// Package config loads slskdpp settings from a YAML file with SLSKDPP_*
// environment variable overrides.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	Shares      []string    `yaml:"shares"`
	LogLevel    string      `yaml:"log_level"`
}

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
		LogLevel:    "info",
	}
}

// Load reads path (if it exists), applies environment overrides and
// fills derived defaults. A missing file is not an error.
func Load(path string) (Config, error) {
	cfg := Default()
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
	if err := applyEnv(&cfg); err != nil {
		return cfg, err
	}
	if cfg.Directories.Downloads == "" {
		cfg.Directories.Downloads = filepath.Join(cfg.Directories.Data, "downloads")
	}
	if cfg.Directories.Incomplete == "" {
		cfg.Directories.Incomplete = filepath.Join(cfg.Directories.Data, "incomplete")
	}
	return cfg, nil
}

func applyEnv(cfg *Config) error {
	strs := map[string]*string{
		"SLSKDPP_USERNAME":       &cfg.Soulseek.Username,
		"SLSKDPP_PASSWORD":       &cfg.Soulseek.Password,
		"SLSKDPP_SERVER":         &cfg.Soulseek.Server,
		"SLSKDPP_DATA_DIR":       &cfg.Directories.Data,
		"SLSKDPP_DOWNLOADS_DIR":  &cfg.Directories.Downloads,
		"SLSKDPP_INCOMPLETE_DIR": &cfg.Directories.Incomplete,
		"SLSKDPP_WEB_LISTEN":     &cfg.Web.Listen,
		"SLSKDPP_WEB_USERNAME":   &cfg.Web.Username,
		"SLSKDPP_WEB_PASSWORD":   &cfg.Web.Password,
		"SLSKDPP_LOG_LEVEL":      &cfg.LogLevel,
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
	return nil
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
	return errors.Join(errs...)
}
