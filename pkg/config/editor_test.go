package config

import (
	"strings"
	"testing"
)

func TestResolveEditor(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if got := ResolveEditor(env(map[string]string{"VISUAL": "code -w", "EDITOR": "vi"}), "darwin"); strings.Join(got, " ") != "code -w" {
		t.Fatal(got)
	}
	if got := ResolveEditor(env(map[string]string{"EDITOR": "nvim"}), "linux"); strings.Join(got, " ") != "nvim" {
		t.Fatal(got)
	}
	if got := ResolveEditor(env(nil), "darwin"); strings.Join(got, " ") != "open -t" {
		t.Fatal(got)
	}
	if got := ResolveEditor(env(nil), "linux"); strings.Join(got, " ") != "xdg-open" {
		t.Fatal(got)
	}
}
