package config

import "testing"

func TestMatcher(t *testing.T) {
	m, errs := NewMatcher([]string{
		"sandbox-proj",
		"prod.tmp_*",
		"prod.events.events_2*",
		"!prod.tmp_keep",
		"# a comment",
		"",
		"**.scratch_*",
	})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	type q struct{ p, d, t string }
	ignored := []q{
		{"sandbox-proj", "", ""},
		{"sandbox-proj", "any", ""},
		{"sandbox-proj", "any", "tbl"},
		{"prod", "tmp_a", ""},
		{"prod", "tmp_a", "t"},
		{"prod", "events", "events_20240101"},
		{"prod", "scratch_x", ""},
		{"other", "scratch_y", "t"},
	}
	kept := []q{
		{"prod", "", ""},
		{"prod", "events", ""},
		{"prod", "events", "events_latest"},
		{"prod", "events", "clicks"},
		{"prod", "tmp_keep", ""},
		{"prod", "tmp_keep", "t"},
		{"prod", "core", "tmp_table"},
		{"sandbox", "", ""},
		{"other", "core", "t"},
	}
	for _, c := range ignored {
		if !m.Ignored(c.p, c.d, c.t) {
			t.Errorf("%v should be ignored", c)
		}
	}
	for _, c := range kept {
		if m.Ignored(c.p, c.d, c.t) {
			t.Errorf("%v should be kept", c)
		}
	}
}

func TestMatcherStarStaysInSegment(t *testing.T) {
	m, _ := NewMatcher([]string{"prod.*"})
	if !m.Ignored("prod", "ds", "") {
		t.Error("prod.* ignores datasets")
	}
	if m.Ignored("prod", "", "") {
		t.Error("prod.* must not ignore the project itself")
	}
	m2, _ := NewMatcher([]string{"p*"})
	if m2.Ignored("p.x", "", "") {
		t.Error("single star must not cross a dot")
	}
}

func TestMatcherDomainScopedProject(t *testing.T) {
	m, _ := NewMatcher([]string{"example.com:proj", "other.com:proj.tmp_*"})
	if !m.Ignored("example.com:proj", "ds", "t") {
		t.Error("dotted project id must match literally")
	}
	if !m.Ignored("other.com:proj", "tmp_a", "") || m.Ignored("other.com:proj", "core", "") {
		t.Error("dataset pattern under dotted project")
	}
}

func TestMatcherLastWins(t *testing.T) {
	m, _ := NewMatcher([]string{"!p.d", "p.*"})
	if !m.Ignored("p", "d", "") {
		t.Error("later broader ignore wins over earlier negation")
	}
}

func TestMatcherErrors(t *testing.T) {
	_, errs := NewMatcher([]string{"ok", ".lead", "trail.", "a..b", "!", "bad\x01"})
	if len(errs) != 5 {
		t.Fatalf("%v", errs)
	}
	for i, want := range []int{1, 2, 3, 4, 5} {
		if errs[i].Index != want {
			t.Errorf("error %d index %d, want %d", i, errs[i].Index, want)
		}
	}
}

func TestNilMatcher(t *testing.T) {
	var m *Matcher
	if m.Ignored("a", "b", "c") {
		t.Fatal("nil matcher ignores nothing")
	}
}

func TestTightening(t *testing.T) {
	if !Tightening([]string{"a"}, []string{"a", "b"}) {
		t.Error("appending an ignore is tightening")
	}
	if Tightening([]string{"a"}, []string{"a", "!b"}) {
		t.Error("appending a negation is not tightening")
	}
	if Tightening([]string{"a", "b"}, []string{"a"}) {
		t.Error("removing a pattern is not tightening")
	}
	if Tightening([]string{"a", "b"}, []string{"b", "a"}) {
		t.Error("reordering is not tightening")
	}
	if !Tightening([]string{"a", "# c"}, []string{"a"}) {
		t.Error("comments are ignored")
	}
}
