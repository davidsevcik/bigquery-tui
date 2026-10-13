package tui

import (
	"fmt"
	"os"
	"testing"
)

// TestDump prints screens for eyeballing: DUMP=1 go test ./internal/tui -run TestDump -v
func TestDump(t *testing.T) {
	if os.Getenv("DUMP") == "" {
		t.Skip("set DUMP=1")
	}
	show := func(title string, r *rig) { fmt.Printf("\n===== %s (%dx%d)\n%s", title, r.m.w, r.m.h, r.screen()) }
	r := newRig(t, 120, 30)
	show("miller start", r)
	r.press("l", "l")
	show("miller on a table", r)
	r.press("p")
	show("after preview", r)
	r.press("L")
	show("tree", r)
	r.press("L")
	show("flat", r)
	r.press("/", "ord")
	show("flat filtered", r)
	r = newRig(t, 80, 24)
	r.press("/", "ord col:amount")
	show("search overlay", r)
	r = newRig(t, 100, 30)
	r.press("ctrl+t")
	show("jump", r)
	r.press("esc", "ctrl+p")
	show("palette", r)
	r.press("esc", "?")
	show("help", r)
	r = newRig(t, 60, 20)
	r.press("l", "l")
	show("narrow", r)
	r.press("enter")
	show("narrow zoom", r)
}
