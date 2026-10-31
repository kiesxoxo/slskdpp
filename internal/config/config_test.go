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
