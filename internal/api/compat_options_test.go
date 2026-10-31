package api

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kiesxoxo/slskdpp/internal/config"
	"github.com/kiesxoxo/slskdpp/internal/logs"
)

const testYAML = `soulseek:
  username: me
  password: hunter2
  listen_port: 2234
`

// optionsEnv has a config file, and remote configuration as asked.
func optionsEnv(t *testing.T, remote bool) (*env, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "slskdpp.yaml")
	os.WriteFile(path, []byte(testYAML), 0o640)
	settings, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	settings.Shares = []string{"Music=/srv/music", "/srv/other"}
	settings.Web.APIKeys = []string{"config-key-123"}
	settings.Web.RemoteConfiguration = remote
	settings.Transfers.UploadSpeedLimit = 500
	e := newEnvWith(t, func(_ *Config, d *Deps) { d.Settings = settings })
	e.login(t)
	return e, path
}

func TestV0OptionsView(t *testing.T) {
	e, _ := optionsEnv(t, false)
	resp, b := e.do(t, "GET", "/api/v0/options", nil)
	var o map[string]any
	json.Unmarshal(b, &o)
	get := func(path string) any {
		var v any = o
		for _, k := range strings.Split(path, ".") {
			m, _ := v.(map[string]any)
			v = m[k]
		}
		return v
	}
	for path, want := range map[string]any{
		"soulseek.username":                           "me",
		"soulseek.password":                           "*****",
		"soulseek.address":                            "server.slsknet.org",
		"soulseek.port":                               2242.0,
		"soulseek.listenPort":                         2234.0,
		"soulseek.listenIpAddress":                    "0.0.0.0",
		"web.port":                                    5030.0,
		"web.authentication.username":                 "slskdpp",
		"web.authentication.password":                 "*****",
		"web.authentication.apiKeys.key1.key":         "*****",
		"global.upload.speedLimit":                    500.0,
		"global.download.speedLimit":                  2147483647.0,
		"remoteConfiguration":                         false,
		"transfers.download.destination.subdirectory": "${SOURCE_DIRECTORY}",
	} {
		if got := get(path); got != want {
			t.Errorf("%s = %v, want %v", path, got, want)
		}
	}
	if resp.StatusCode != 200 || strings.Contains(string(b), "hunter2") || strings.Contains(string(b), "config-key-123") {
		t.Errorf("options: %d %s", resp.StatusCode, b)
	}
	if dirs, _ := get("shares.directories").([]any); len(dirs) != 2 || dirs[0] != "[Music]/srv/music" || dirs[1] != "/srv/other" {
		t.Errorf("shares %v", get("shares.directories"))
	}
	if resp, _ := e.do(t, "GET", "/api/v0/options/startup", nil); resp.StatusCode != 200 {
		t.Errorf("startup: %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, "GET", "/api/v0/options/debug", nil); resp.StatusCode != 403 {
		t.Errorf("debug: %d", resp.StatusCode)
	}

	// Without remote configuration, nothing that changes or reveals the
	// file works.
	for _, c := range []struct{ method, path string }{
		{"PATCH", "/api/v0/options"}, {"GET", "/api/v0/options/yaml"}, {"GET", "/api/v0/options/yaml/location"},
		{"PUT", "/api/v0/options/yaml"}, {"POST", "/api/v0/options/yaml/validate"},
	} {
		if resp, _ := e.do(t, c.method, c.path, ""); resp.StatusCode != 403 {
			t.Errorf("%s %s without remote configuration: %d", c.method, c.path, resp.StatusCode)
		}
	}
}

func TestV0OptionsOverlay(t *testing.T) {
	e, _ := optionsEnv(t, true)
	patch := func(body string) (int, []byte) {
		t.Helper()
		resp, b := e.do(t, "PATCH", "/api/v0/options", json.RawMessage(body))
		return resp.StatusCode, b
	}
	if code, _ := patch(`{}`); code != 204 {
		t.Errorf("empty overlay: %d", code)
	}
	if code, _ := patch(`{"soulseek":{"listenPort":70000}}`); code != 400 {
		t.Errorf("bad port: %d", code)
	}
	if code, _ := patch(`{"soulseek":{"listenIpAddress":"nope"}}`); code != 400 {
		t.Errorf("bad address: %d", code)
	}
	if code, _ := patch(`{"soulseek":{"listenPort":1}}`); code != 500 {
		t.Errorf("port that cannot be opened: %d", code)
	}
	code, b := patch(`{"soulseek":{"listenPort":50300}}`)
	if code != 200 || !strings.Contains(string(b), `"listenPort":50300`) || !strings.Contains(string(b), `"listenIpAddress":"0.0.0.0"`) {
		t.Errorf("overlay: %d %s", code, b)
	}
	if ip, port := e.net.ListenAddr(); ip != "" || port != 50300 {
		t.Errorf("listener %q:%d", ip, port)
	}
	// Current options show the move; startup options do not.
	_, b = e.do(t, "GET", "/api/v0/options", nil)
	if !strings.Contains(string(b), `"listenPort":50300`) {
		t.Errorf("current options %s", b)
	}
	_, b = e.do(t, "GET", "/api/v0/options/startup", nil)
	if !strings.Contains(string(b), `"listenPort":2234`) {
		t.Errorf("startup options %s", b)
	}
	if code, _ := patch(`{"soulseek":{"listenIpAddress":"127.0.0.1"}}`); code != 200 {
		t.Errorf("address only: %d", code)
	}
	if ip, port := e.net.ListenAddr(); ip != "127.0.0.1" || port != 50300 {
		t.Errorf("listener %q:%d after moving the address only", ip, port)
	}
}

func TestV0OptionsYAML(t *testing.T) {
	e, path := optionsEnv(t, true)
	var loc, got string
	if resp, b := e.do(t, "GET", "/api/v0/options/yaml/location", nil); resp.StatusCode != 200 || json.Unmarshal(b, &loc) != nil || loc != path {
		t.Errorf("location: %d %s", resp.StatusCode, b)
	}
	if resp, b := e.do(t, "GET", "/api/v0/options/yaml", nil); resp.StatusCode != 200 || json.Unmarshal(b, &got) != nil || got != testYAML {
		t.Errorf("yaml: %d %s", resp.StatusCode, b)
	}
	// Reading and writing the file takes a login, not an API key.
	if code, _ := e.bearer(t, "GET", "/api/v0/options/yaml", "config-key-123", ""); code != 403 {
		t.Errorf("yaml with an API key: %d", code)
	}

	var msg string
	resp, b := e.do(t, "POST", "/api/v0/options/yaml/validate", "soulseek:\n  usernme: typo\n")
	json.Unmarshal(b, &msg)
	if resp.StatusCode != 200 || !strings.Contains(msg, "usernme") {
		t.Errorf("validate a typo: %d %s", resp.StatusCode, b)
	}
	if resp, b := e.do(t, "POST", "/api/v0/options/yaml/validate", testYAML); resp.StatusCode != 200 || len(strings.TrimSpace(string(b))) != 0 {
		t.Errorf("validate a good file: %d %q", resp.StatusCode, b)
	}
	if resp, _ := e.do(t, "PUT", "/api/v0/options/yaml", "soulseek:\n  listen_port: 0\n"); resp.StatusCode != 400 {
		t.Errorf("write a bad file: %d", resp.StatusCode)
	}
	if b, _ := os.ReadFile(path); string(b) != testYAML {
		t.Error("a bad file was written")
	}

	updated := testYAML + "log_level: debug\n"
	if resp, b := e.do(t, "PUT", "/api/v0/options/yaml", updated); resp.StatusCode != 200 {
		t.Fatalf("write: %d %s", resp.StatusCode, b)
	}
	if b, _ := os.ReadFile(path); string(b) != updated {
		t.Errorf("file %q", b)
	}
	if b, _ := os.ReadFile(path + ".bak"); string(b) != testYAML {
		t.Errorf("backup %q", b)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o640 {
		t.Errorf("mode %v", st.Mode().Perm())
	}
	_, b = e.do(t, "GET", "/api/v0/application", nil)
	if !strings.Contains(string(b), `"pendingRestart":true`) {
		t.Errorf("no pending restart after rewriting the config: %s", b)
	}
}

func TestV0Logs(t *testing.T) {
	dir := t.TempDir()
	h, err := logs.New(logs.Config{Dir: dir, Stderr: &strings.Builder{}})
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(h)
	log.With("component", "server").Info("logged in", "user", "me")
	log.Warn("careful")
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, ".hidden.log"), []byte("x"), 0o644)

	e := newEnvWith(t, func(_ *Config, d *Deps) { d.Logs = h })
	e.login(t)
	var live []struct{ Timestamp, Context, Level, Message string }
	_, b := e.do(t, "GET", "/api/v0/logs/live", nil)
	json.Unmarshal(b, &live)
	if len(live) != 2 || live[0].Context != "server" || live[0].Level != "Information" || live[0].Message != "logged in user=me" ||
		live[1].Level != "Warning" || live[1].Context != "slskdpp" {
		t.Fatalf("live %s", b)
	}
	if _, err := time.Parse(time.RFC3339Nano, live[0].Timestamp); err != nil {
		t.Errorf("timestamp %q", live[0].Timestamp)
	}

	today := "slskdpp-" + time.Now().Format("20060102") + ".log"
	var files []struct {
		Name   string
		Length int64
	}
	_, b = e.do(t, "GET", "/api/v0/logs/files", nil)
	json.Unmarshal(b, &files)
	if len(files) != 1 || files[0].Name != today || files[0].Length == 0 {
		t.Errorf("files %s", b)
	}
	resp, b := e.do(t, "GET", "/api/v0/logs/files/"+today, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(b), "logged in") || resp.Header.Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Errorf("file: %d %q %s", resp.StatusCode, resp.Header.Get("Content-Type"), b)
	}
	for name, want := range map[string]int{"notes.txt": 403, "nope.log": 404, "%2E%2E%2Fx.log": 400} {
		if resp, _ := e.do(t, "GET", "/api/v0/logs/files/"+name, nil); resp.StatusCode != want {
			t.Errorf("%s: %d, want %d", name, resp.StatusCode, want)
		}
	}
	// The router cleans "/.." away before the handler sees it.
	if resp, _ := e.do(t, "GET", "/api/v0/logs/files/..", nil); resp.StatusCode == 200 {
		t.Error(`"/files/.." was served`)
	}

	// Without a log source there is nothing, but no error for live.
	e = newEnv(t)
	e.login(t)
	if resp, b := e.do(t, "GET", "/api/v0/logs/live", nil); resp.StatusCode != 200 || strings.TrimSpace(string(b)) != "[]" {
		t.Errorf("live without logs: %d %s", resp.StatusCode, b)
	}
	if resp, _ := e.do(t, "GET", "/api/v0/logs/files", nil); resp.StatusCode != 404 {
		t.Errorf("files without logs: %d", resp.StatusCode)
	}
}
