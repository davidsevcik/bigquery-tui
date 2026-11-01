// Package version reports the build version of bqt.
package version

import "runtime/debug"

// Version is set at build time with -ldflags "-X .../internal/version.Version=v1.2.3".
var Version = ""

// String returns the version, falling back to the build information.
func String() string {
	if Version != "" {
		return Version
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	return fromBuildInfo(bi)
}

// fromBuildInfo prefers the module version that `go install …@v1.2.3` records,
// then the VCS revision of a local build.
func fromBuildInfo(bi *debug.BuildInfo) string {
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	var rev, modified string
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}
	if rev == "" {
		return "dev"
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if modified == "true" {
		rev += "-dirty"
	}
	return "dev-" + rev
}
