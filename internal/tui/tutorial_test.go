package tui

import (
	"testing"

	"github.com/davidsevcik/bigquery-tui/internal/bq/bqtest"
)

func tutorialRig(t *testing.T, w, h int) *rig {
	t.Helper()
	rigEditing = "vim"
	defer func() { rigEditing = "plain" }()
	r := newRig(t, w, h)
	r.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.Numbers(7) }
	return r
}

func (r *rig) tutStep(want int) {
	r.t.Helper()
	if r.m.tut == nil {
		r.t.Fatalf("the tutorial ended, want step %d", want+1)
	}
	if r.m.tut.step != want {
		r.t.Fatalf("tutorial step %d (%s), want %d (%s)", r.m.tut.step+1, tutSteps[r.m.tut.step].title, want+1, tutSteps[want].title)
	}
	if n := len(r.m.render()); n != r.m.termH {
		r.t.Fatalf("%d lines for a %d-row terminal", n, r.m.termH)
	}
}

func TestTutorialWalkthrough(t *testing.T) {
	r := tutorialRig(t, 120, 36)
	r.press("ctrl+o")
	r.press("ctrl+g")
	r.tutStep(0)
	if r.m.zone != ZoneExplorer || r.m.h != 36-tutorialRows {
		t.Fatalf("zone %s, height %d", r.m.zone, r.m.h)
	}
	r.contains("Tutorial 1/18 · Welcome", "ctrl+g then: n next")
	r.press("j")
	r.tutStep(0)
	r.press("k", "ctrl+g", "n")
	r.tutStep(1)
	r.press("j")
	r.tutStep(2)
	r.contains("✓ Move in the Explorer done")
	r.press("l")
	r.tutStep(2)
	r.press("h")
	r.tutStep(3)
	r.press("/")
	r.typeText("staging")
	r.press("esc")
	r.tutStep(3)
	r.contains("Projects 1/1 of 2 ▸ sta")
	r.press("esc")
	r.tutStep(4)
	r.press("ctrl+t")
	r.typeText("orders")
	r.press("enter")
	r.tutStep(5)
	r.press("L")
	r.tutStep(6)
	r.press("L", "L")
	r.press("enter")
	if r.m.zone != ZoneSchema {
		t.Fatal("enter on a table goes to the Schema pane")
	}
	r.press("j")
	r.tutStep(6)
	r.press("p")
	r.tutStep(7)
	r.contains("Sample · free read")
	r.press("esc")
	r.press("ctrl+p", "esc")
	r.tutStep(8)
	r.press("?", "esc")
	r.tutStep(9)
	r.press("ctrl+o")
	r.tutStep(9)
	r.press("ctrl+o")
	r.tutStep(10)
	r.contains("Arrange the panes", "ctrl+w space", "ctrl+w z")
	for range presets {
		r.press("ctrl+w", "space")
	}
	r.contains("layout: classic")
	r.tutStep(10)
	r.press("ctrl+w", "z")
	r.tutStep(10)
	r.contains("(maximised)")
	r.press("ctrl+w", "z")
	r.tutStep(11)
	r.contains("vim keys")
	r.press("i")
	r.typeText("SELECT 1")
	r.tutStep(11)
	r.press("esc")
	r.tutStep(12)
	r.press("enter")
	r.tutStep(13)
	r.press("ctrl+w", "j")
	r.tutStep(14)
	r.press("enter")
	if r.m.ov != ovRecord {
		t.Fatal("enter opens the record")
	}
	r.press("esc")
	r.tutStep(15)
	r.press("ctrl+w", "h")
	r.tutStep(15)
	r.press("ctrl+w", "k")
	r.tutStep(16)
	r.press("ctrl+x", "r", "esc")
	r.tutStep(17)
	r.press("ctrl+g", "n")
	if r.m.tut != nil || r.m.h != 36 {
		t.Fatalf("the last step closes the tutorial and gives the rows back (%d)", r.m.h)
	}
	r.contains("tutorial finished")
}

func TestTutorialKeys(t *testing.T) {
	r := tutorialRig(t, 80, 24)
	r.press("ctrl+g", "ctrl+g", "n", "ctrl+g", "n")
	r.tutStep(2)
	r.golden("tutorial_80x24")
	r.press("ctrl+g")
	r.contains("tutorial key: n next")
	r.press("p")
	r.tutStep(1)
	r.press("ctrl+g", "x")
	r.contains("ctrl+g x is not a tutorial key")
	r.tutStep(1)
	r.press("ctrl+g", "r")
	r.tutStep(0)
	r.press("ctrl+w", "l", "i", "ctrl+g", "n")
	r.tutStep(1)
	if r.text() != "" {
		t.Fatalf("the tutorial keys do not type: %q", r.text())
	}
	r.press("esc")
	r.typeText(":")
	r.press("ctrl+g", "q")
	if r.m.tut != nil || r.m.h != 24 || r.m.ov != ovPalette {
		t.Fatal("ctrl+g q closes the tutorial and leaves the palette open")
	}
	r.typeText("tutorial")
	r.press("enter")
	r.tutStep(0)
}

func TestTutorialWaitsForAPopupQuestion(t *testing.T) {
	r := tutorialRig(t, 100, 30)
	r.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.QueryScript{StatementType: "DROP_TABLE"} }
	r.press("ctrl+w", "l")
	r.typeText("iDROP TABLE x")
	r.press("esc", "enter")
	if r.m.ov != ovConfirm {
		t.Fatal("the run asks for confirmation")
	}
	r.press("ctrl+g")
	if r.m.tut != nil || r.m.ov != ovConfirm {
		t.Fatal("ctrl+g leaves the question open")
	}
	r.contains("answer the popup first")
}

func TestTutorialResize(t *testing.T) {
	r := tutorialRig(t, 100, 30)
	r.press("ctrl+g")
	r.send(teaSize(90, 40))
	if r.m.h != 40-tutorialRows || r.m.termH != 40 {
		t.Fatalf("h %d termH %d", r.m.h, r.m.termH)
	}
	r.tutStep(0)
}

func TestTutorialFilterStepNeedsAFilter(t *testing.T) {
	r := tutorialRig(t, 120, 36)
	r.press("ctrl+g", "ctrl+g", "n", "j", "l", "h")
	r.tutStep(3)
	r.press("/", "esc")
	r.tutStep(3)
}
