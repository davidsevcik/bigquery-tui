// Package archtest enforces the architecture rules from spec section 3: the
// core (every package under pkg/) imports no UI libraries, no frontend
// packages and nothing under internal/, and the shared api types depend on
// nothing else in the module.
package archtest

import (
	"os/exec"
	"strings"
	"testing"
)

const module = "github.com/davidsevcik/bigquery-tui"

// forbiddenPrefixes are what no core package may (transitively) import. The
// core is public so other modules (a GUI) can import it; anything it pulled in
// from internal/ would leak types those modules cannot name.
var forbiddenPrefixes = []string{
	"charm.land/",
	"github.com/charmbracelet/",
	"github.com/muesli/termenv",
	"github.com/modelcontextprotocol/",
	module + "/internal/",
	module + "/cmd/",
}

func goList(t *testing.T, args ...string) []string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go tool not on PATH")
	}
	out, err := exec.Command("go", append([]string{"list"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("go list %v: %v\n%s", args, err, out)
	}
	return strings.Fields(string(out))
}

func TestCoreImportsNoUIOrFrontends(t *testing.T) {
	core := goList(t, "../../pkg/...")
	if len(core) == 0 {
		t.Fatal("no packages under pkg/")
	}
	for _, pkg := range core {
		for _, d := range goList(t, "-deps", "-f", "{{.ImportPath}}", pkg) {
			for _, bad := range forbiddenPrefixes {
				if strings.HasPrefix(d, bad) {
					t.Errorf("%s must not depend on %s (matches %q)", pkg, d, bad)
				}
			}
		}
	}
}

func TestAPITypesAreSelfContained(t *testing.T) {
	for _, d := range goList(t, "-deps", "-f", "{{.ImportPath}}", "../../pkg/api") {
		if strings.HasPrefix(d, module) && d != module+"/pkg/api" {
			t.Errorf("pkg/api must not import other module packages, found %s", d)
		}
	}
}
