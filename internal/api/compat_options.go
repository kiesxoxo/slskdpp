package api

// slskd's /api/v0/options: the configuration as slskd's options, a
// runtime overlay for the listen address, and reading and rewriting the
// YAML file. Changing anything needs web.remote_configuration, as slskd
// needs remoteConfiguration.

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/kiesxoxo/slskdpp/internal/config"
	"github.com/kiesxoxo/slskdpp/internal/shares"
)

const v0Redacted = "*****"

// v0Overlay is slskd's OptionsOverlay: the listen address, changed at
// runtime and not saved.
type v0Overlay struct {
	Soulseek *struct {
		ListenIPAddress *string `json:"listenIpAddress"`
		ListenPort      *int    `json:"listenPort"`
	} `json:"soulseek"`
}

type v0OptionsState struct {
	mu             sync.Mutex
	pendingRestart bool // the YAML file was rewritten
}

func (a *API) v0RemoteConfig(w http.ResponseWriter) bool {
	if !a.settings.Web.RemoteConfiguration {
		writeError(w, http.StatusForbidden, "remote configuration is off; set web.remote_configuration to allow it")
		return false
	}
	return true
}

// v0NoKey refuses API keys, for what slskd allows only with a login.
func v0NoKey(w http.ResponseWriter, r *http.Request) bool {
	if viaKey(r) {
		writeError(w, http.StatusForbidden, "API keys cannot do this; log in instead")
		return false
	}
	return true
}

// v0Limit is a speed limit in KiB/s, with slskd's int.MaxValue for none.
func v0Limit(kib int64) int64 {
	if kib <= 0 {
		return math.MaxInt32
	}
	return kib
}

// v0OptionsFrom shows cfg as slskd's Options, secrets redacted. listenIP
// and listenPort are the listener's, which an overlay may have moved.
func (a *API) v0OptionsFrom(cfg config.Config, listenIP string, listenPort uint32) map[string]any {
	host, portStr, err := net.SplitHostPort(cfg.Soulseek.Server)
	if err != nil {
		host = cfg.Soulseek.Server
	}
	serverPort, _ := strconv.Atoi(portStr)
	_, webPortStr, _ := net.SplitHostPort(cfg.Web.Listen)
	webPort, _ := strconv.Atoi(webPortStr)
	if listenIP == "" {
		listenIP = "0.0.0.0"
	}
	shareDirs := []string{}
	for _, s := range cfg.Shares {
		root := shares.ParseRoot(s)
		if root.Alias != "" {
			shareDirs = append(shareDirs, "["+root.Alias+"]"+root.Path)
		} else {
			shareDirs = append(shareDirs, root.Path)
		}
	}
	apiKeys := map[string]any{}
	for i := range cfg.Web.APIKeys {
		apiKeys["key"+strconv.Itoa(i+1)] = map[string]any{"key": v0Redacted, "role": "Administrator", "cidr": "0.0.0.0/0,::/0"}
	}
	return map[string]any{
		"debug":                false,
		"headless":             false,
		"remoteConfiguration":  cfg.Web.RemoteConfiguration,
		"remoteFileManagement": cfg.Web.RemoteFileManagement,
		"instanceName":         "default",
		"directories":          map[string]any{"downloads": cfg.Directories.Downloads, "incomplete": cfg.Directories.Incomplete},
		"shares":               map[string]any{"directories": shareDirs, "filters": []string{}},
		"global": map[string]any{
			"upload":   map[string]any{"slots": cfg.Transfers.UploadSlots, "speedLimit": v0Limit(cfg.Transfers.UploadSpeedLimit)},
			"download": map[string]any{"slots": math.MaxInt32, "speedLimit": v0Limit(cfg.Transfers.DownloadSpeedLimit)},
		},
		// Files queued without a destination go into a folder named after
		// their remote parent folder, as with slskd's default.
		"transfers": map[string]any{"download": map[string]any{"destination": map[string]any{"subdirectory": "${SOURCE_DIRECTORY}"}}},
		"rooms":     []string{},
		"metrics": map[string]any{
			"enabled": cfg.Metrics.Enabled,
			"url":     cmp.Or(cfg.Metrics.Path, "/metrics"),
			"authentication": map[string]any{
				// Without its own login it needs an API key; never open.
				"disabled": false,
				"username": cfg.Metrics.Username,
				"password": v0Redacted,
			},
		},
		"web": map[string]any{
			"port":    webPort,
			"urlBase": cmp.Or(config.CleanURLBase(cfg.Web.URLBase), "/"),
			"authentication": map[string]any{
				"disabled": false,
				"username": cfg.Web.Username,
				"password": v0Redacted,
				"jwt":      map[string]any{"key": v0Redacted, "ttl": jwtTTL.Milliseconds()},
				"apiKeys":  apiKeys,
			},
		},
		"soulseek": map[string]any{
			"address":         host,
			"port":            serverPort,
			"username":        cfg.Soulseek.Username,
			"password":        v0Redacted,
			"listenIpAddress": listenIP,
			"listenPort":      listenPort,
		},
	}
}

func (a *API) v0Options(w http.ResponseWriter, r *http.Request) {
	ip, port := a.net.ListenAddr()
	v0JSON(w, r, http.StatusOK, a.v0OptionsFrom(a.settings, ip, port))
}

func (a *API) v0StartupOptions(w http.ResponseWriter, r *http.Request) {
	v0JSON(w, r, http.StatusOK, a.v0OptionsFrom(a.settings, "", a.settings.Soulseek.ListenPort))
}

// v0DebugOptions is slskd's configuration debug view, which it only
// shows in debug mode; slskdpp has none.
func (a *API) v0DebugOptions(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusForbidden, "the configuration debug view is not available")
}

// v0PatchOptions applies slskd's options overlay: the listen address and
// port, moved now and not saved.
func (a *API) v0PatchOptions(w http.ResponseWriter, r *http.Request) {
	if !a.v0RemoteConfig(w) {
		return
	}
	var o v0Overlay
	if r.ContentLength != 0 {
		if !readJSON(w, r, &o) {
			return
		}
	}
	if o.Soulseek == nil || (o.Soulseek.ListenIPAddress == nil && o.Soulseek.ListenPort == nil) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	ip, port := a.net.ListenAddr()
	if p := o.Soulseek.ListenPort; p != nil {
		if *p < 1 || *p > 65535 {
			writeError(w, http.StatusBadRequest, "soulseek.listenPort must be between 1 and 65535")
			return
		}
		port = uint32(*p)
	}
	if s := o.Soulseek.ListenIPAddress; s != nil {
		addr, err := netip.ParseAddr(*s)
		if err != nil {
			writeError(w, http.StatusBadRequest, "soulseek.listenIpAddress is not an IP address")
			return
		}
		ip = addr.String()
		if addr.IsUnspecified() && addr.Is4() {
			ip = ""
		}
	}
	if err := a.net.SetListen(ip, port); err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to apply options patch: "+err.Error())
		return
	}
	a.log.Info("listen address changed through the API", "ip", ip, "port", port)
	shown := ip
	if shown == "" {
		shown = "0.0.0.0"
	}
	v0JSON(w, r, http.StatusOK, map[string]any{"soulseek": map[string]any{"listenIpAddress": shown, "listenPort": port}})
}

// YAML.

func (a *API) v0YAMLLocation(w http.ResponseWriter, r *http.Request) {
	if !a.v0RemoteConfig(w) || !v0NoKey(w, r) {
		return
	}
	path, err := filepath.Abs(a.settings.Path)
	if err != nil || a.settings.Path == "" {
		writeError(w, http.StatusNotFound, "slskdpp was started without a config file")
		return
	}
	v0JSON(w, r, http.StatusOK, path)
}

func (a *API) v0GetYAML(w http.ResponseWriter, r *http.Request) {
	if !a.v0RemoteConfig(w) || !v0NoKey(w, r) {
		return
	}
	b, err := os.ReadFile(a.settings.Path)
	switch {
	case a.settings.Path == "" || errors.Is(err, fs.ErrNotExist):
		writeError(w, http.StatusNotFound, "there is no config file")
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
	default:
		v0JSON(w, r, http.StatusOK, string(b))
	}
}

// v0CheckYAML returns why yaml is not a usable config, or "".
func v0CheckYAML(yaml string) string {
	cfg, err := config.Parse([]byte(yaml))
	if err != nil {
		return err.Error()
	}
	if err := cfg.ApplyEnv(); err != nil {
		return err.Error()
	}
	if err := cfg.Validate(); err != nil {
		return err.Error()
	}
	return ""
}

// v0PutYAML checks and writes the config file, keeping the old one as
// .bak. slskdpp reads its config at startup, so the change applies on
// the next restart, which the application state reports as pending.
func (a *API) v0PutYAML(w http.ResponseWriter, r *http.Request) {
	if !a.v0RemoteConfig(w) || !v0NoKey(w, r) {
		return
	}
	if a.settings.Path == "" {
		writeError(w, http.StatusNotFound, "slskdpp was started without a config file")
		return
	}
	var yaml string
	if !readJSON(w, r, &yaml) {
		return
	}
	if msg := v0CheckYAML(yaml); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	if err := v0WriteConfig(a.settings.Path, yaml); err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to update configuration file: "+err.Error())
		return
	}
	a.v0Opts.mu.Lock()
	a.v0Opts.pendingRestart = true
	a.v0Opts.mu.Unlock()
	a.log.Warn("config file rewritten through the API; restart to apply it", "path", a.settings.Path)
	w.WriteHeader(http.StatusOK)
}

// v0WriteConfig replaces the file at path, keeping its mode, and copies
// the old contents to path.bak first.
func v0WriteConfig(path, contents string) error {
	mode := fs.FileMode(0o600)
	if old, err := os.ReadFile(path); err == nil {
		if st, err := os.Stat(path); err == nil {
			mode = st.Mode().Perm()
		}
		if err := os.WriteFile(path+".bak", old, mode); err != nil {
			return fmt.Errorf("backing up: %w", err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(contents); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// v0ValidateYAML answers 200 either way, with the problem as the body if
// there is one, as slskd does.
func (a *API) v0ValidateYAML(w http.ResponseWriter, r *http.Request) {
	if !a.v0RemoteConfig(w) {
		return
	}
	var yaml string
	if !readJSON(w, r, &yaml) {
		return
	}
	if msg := v0CheckYAML(yaml); msg != "" {
		v0JSON(w, r, http.StatusOK, strings.TrimSpace(msg))
		return
	}
	w.WriteHeader(http.StatusOK)
}
