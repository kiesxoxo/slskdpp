// Package version holds the release version, set at build time with
// -ldflags "-X github.com/kiesxoxo/slskdpp/internal/version.Version=v1.2.3".
package version

import "runtime/debug"

// Version is the release this binary was built from.
var Version = "dev"

// String is Version, or the module version and VCS revision Go recorded
// when it was not set at build time.
func String() string {
	if Version != "dev" {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return v
		}
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 12 {
				return "dev-" + s.Value[:12]
			}
		}
	}
	return Version
}
