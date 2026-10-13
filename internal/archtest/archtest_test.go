// Package archtest enforces the architecture rules from spec section 3: the
// core imports no UI libraries and no frontend packages, and the shared api
// types depend on nothing else in the module.
package archtest

import (
	"os/exec"
	"strings"
	"testing"
)

const module = "github.com/davidsevcik/bigquery-tui"

// corePackages must never (transitively) import a UI library or a frontend.
var corePackages = []string{
	"internal/api", "internal/config", "internal/store", "internal/sync",
	"internal/bq", "internal/bq/bqtest", "internal/engine", "internal/logging", "internal/version",
	"internal/search", "internal/format",
}

var forbiddenPrefixes = []string{
	"charm.land/",
	"github.com/charmbracelet/",
	"github.com/muesli/termenv",
	module + "/internal/cli",
	module + "/internal/tui",
	module + "/internal/mcp",
	module + "/cmd/",
}

func deps(t *testing.T, pkg string) []string {
	t.Helper()
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", "./"+pkg).CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps %s: %v\n%s", pkg, err, out)
	}
	return strings.Fields(string(out))
}

func TestCoreImportsNoUIOrFrontends(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go tool not on PATH")
	}
	for _, pkg := range corePackages {
		for _, d := range deps(t, "../../"+pkg) {
			for _, bad := range forbiddenPrefixes {
				if strings.HasPrefix(d, bad) {
					t.Errorf("%s must not depend on %s (matches %q)", pkg, d, bad)
				}
			}
		}
	}
}

func TestAPITypesAreSelfContained(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go tool not on PATH")
	}
	for _, d := range deps(t, "../../internal/api") {
		if strings.HasPrefix(d, module) && d != module+"/internal/api" {
			t.Errorf("internal/api must not import other module packages, found %s", d)
		}
	}
}
