package version

import (
	"runtime/debug"
	"testing"
)

func TestFromBuildInfo(t *testing.T) {
	vcs := func(rev, modified string) []debug.BuildSetting {
		return []debug.BuildSetting{{Key: "vcs.revision", Value: rev}, {Key: "vcs.modified", Value: modified}}
	}
	cases := []struct {
		name string
		bi   debug.BuildInfo
		want string
	}{
		{"go install of a tag", debug.BuildInfo{Main: debug.Module{Version: "v0.1.0"}}, "v0.1.0"},
		{"go install of a commit", debug.BuildInfo{Main: debug.Module{Version: "v0.1.1-0.20261101120000-abcdef123456"}}, "v0.1.1-0.20261101120000-abcdef123456"},
		{"local build", debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: vcs("0123456789abcdef", "false")}, "dev-0123456789ab"},
		{"local build with changes", debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: vcs("0123456789abcdef", "true")}, "dev-0123456789ab-dirty"},
		{"no VCS information", debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, "dev"},
	}
	for _, c := range cases {
		if got := fromBuildInfo(&c.bi); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
