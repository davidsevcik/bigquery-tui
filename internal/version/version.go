// Package version reports the build version of bqt.
package version

import "runtime/debug"

// Version is set at build time with -ldflags "-X .../internal/version.Version=v1.2.3".
var Version = ""

// String returns the version, falling back to VCS information from the build.
func String() string {
	if Version != "" {
		return Version
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		var rev, modified string
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				modified = s.Value
			}
		}
		if rev != "" {
			if len(rev) > 12 {
				rev = rev[:12]
			}
			if modified == "true" {
				rev += "-dirty"
			}
			return "dev-" + rev
		}
	}
	return "dev"
}
