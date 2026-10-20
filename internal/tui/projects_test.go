package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestProjectsDialogTurnsProjectsOnAndOff(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("ctrl+x", "p")
	r.contains("Projects", "[x] acme-prod", "[x] acme-staging", "space turns a project on or off")
	r.press("j", "space")
	r.contains("[x] acme-prod", "[ ] acme-staging", "projects acme-prod")
	r.press("esc")

	// one project on: the Miller project column is gone
	r.lacks("Projects 1/", "acme-staging")
	r.contains("Datasets 1/4", "Tables")
	if s := r.m.sel(); s.path.Project != "acme-prod" || s.path.Dataset == "" {
		t.Fatalf("the cursor starts in the datasets column: %+v", s.path)
	}
	r.press("h")
	if s := r.m.sel(); s.path.Dataset == "" {
		t.Fatal("h does not go back to a hidden project column")
	}

	// search and the tree leave the project out
	r.press("/", "orders")
	r.contains("acme-prod")
	if strings.Contains(r.screen(), "acme-staging") {
		t.Fatalf("a turned-off project is searched:\n%s", r.screen())
	}
	r.press("esc", "L")
	r.lacks("acme-staging")
	r.press("L", "L")

	// the choice is remembered
	m2 := New(r.m.be, Options{NoBackground: true})
	defer m2.Close()
	if !m2.hidden["acme-staging"] || m2.hidden["acme-prod"] {
		t.Fatalf("hidden %v", m2.hidden)
	}

	// the last project on stays on; a turns everything back on
	r.press("ctrl+x", "p", "space")
	if !strings.Contains(r.m.toast, "at least one project stays on") || r.m.hidden["acme-prod"] {
		t.Fatalf("toast %q hidden %v", r.m.toast, r.m.hidden)
	}
	r.press("a", "esc", "h", "h")
	r.contains("Projects 1/2", "acme-staging")
	r.lacks("projects acme-prod")
}

func TestProjectsDialogOnlyThisOne(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("ctrl+x", "p", "j", "o", "esc")
	if !r.m.hidden["acme-prod"] || r.m.hidden["acme-staging"] {
		t.Fatalf("hidden %v", r.m.hidden)
	}
	r.contains("Datasets 1/1", "projects acme-staging")
	r.press("ctrl+t", "orders")
	r.contains("acme-staging.sales")
	r.lacks("acme-prod")
}

func TestHelpWrapsDescriptions(t *testing.T) {
	for _, w := range []int{40, 60, 94} {
		lines := helpLines(w)
		for _, l := range lines {
			if wid(l) > w {
				t.Fatalf("width %d: line is %d wide: %q", w, wid(l), l)
			}
		}
		// a long description is wrapped, not cut
		words := strings.Join(strings.Fields(ansi.Strip(strings.Join(lines, "\n"))), " ")
		if !strings.Contains(words, "zoomed preview: show the row as a record") {
			t.Fatalf("width %d: description cut:\n%s", w, strings.Join(lines, "\n"))
		}
	}
}
