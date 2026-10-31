package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParse(t *testing.T) {
	cfg, err := Parse([]byte("soulseek:\n  username: me\nlog_files: false\n"))
	if err != nil || cfg.Soulseek.Username != "me" || cfg.LogFiles || cfg.LogKeepDays != 7 || cfg.Soulseek.ListenPort != 2234 {
		t.Errorf("parse: %+v %v", cfg, err)
	}
	if _, err := Parse([]byte("soulseek:\n  usernme: typo\n")); err == nil {
		t.Error("unknown key accepted")
	}
	if _, err := Parse(nil); err != nil {
		t.Errorf("empty file: %v", err)
	}
}

func TestLoadKeepsPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "slskdpp.yaml")
	cfg, err := Load(missing)
	if err != nil || cfg.Path != missing || !cfg.LogFiles {
		t.Errorf("missing file: %+v %v", cfg, err)
	}
	os.WriteFile(missing, []byte("log_keep_days: 3\n"), 0o644)
	if cfg, _ := Load(missing); cfg.Path != missing || cfg.LogKeepDays != 3 || cfg.LogDir() != filepath.Join("data", "logs") {
		t.Errorf("file: %+v", cfg)
	}
}

func TestURLBase(t *testing.T) {
	for in, want := range map[string]string{"": "", "/": "", "slskd": "/slskd", "/slskd/": "/slskd", " /a/b/ ": "/a/b"} {
		if got := CleanURLBase(in); got != want {
			t.Errorf("CleanURLBase(%q) = %q, want %q", in, got, want)
		}
	}
	t.Setenv("SLSKDPP_WEB_URL_BASE", "slskd/")
	cfg, err := Load("")
	if err != nil || cfg.Web.URLBase != "/slskd" {
		t.Errorf("env: %q %v", cfg.Web.URLBase, err)
	}
	cfg.Soulseek.Username, cfg.Soulseek.Password = "me", "pw"
	for _, bad := range []string{"/a/../b", "/a?b", "/a b"} {
		cfg.Web.URLBase = bad
		if cfg.Validate() == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestMetrics(t *testing.T) {
	cfg, err := Load("")
	if err != nil || cfg.Metrics.Enabled || cfg.Metrics.Path != "/metrics" {
		t.Errorf("defaults: %+v %v", cfg.Metrics, err)
	}
	t.Setenv("SLSKDPP_METRICS_ENABLED", "true")
	t.Setenv("SLSKDPP_METRICS_USERNAME", "prom")
	if cfg, err := Load(""); err != nil || !cfg.Metrics.Enabled || cfg.Metrics.Username != "prom" {
		t.Errorf("env: %+v %v", cfg.Metrics, err)
	}
	t.Setenv("SLSKDPP_METRICS_ENABLED", "maybe")
	if _, err := Load(""); err == nil {
		t.Error("bad bool accepted")
	}
}
