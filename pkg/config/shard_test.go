package config

import (
	"strings"
	"testing"
)

func TestShardPatterns(t *testing.T) {
	c := mustParse(t, "", LoadOptions{})
	if !c.Shards().Empty() {
		t.Fatal("no patterns by default: nothing collapses")
	}
	c = mustParse(t, "shard_patterns = [\n  '.+(\\d{8})',\n  '.+_([0-9a-f]{24})(?:_history)?',\n]\n", LoadOptions{})
	if got := c.Shards().Sources(); len(got) != 2 || got[0] != `.+(\d{8})` {
		t.Fatalf("%q", got)
	}
	cases := []struct {
		name, src, key, msg string
		line                int
	}{
		{"no capturing group", "location = \"EU\"\nshard_patterns = ['orders_\\d+']\n", "shard_patterns[0]", "exactly one capturing group", 2},
		{"two capturing groups", "shard_patterns = [\n  '.+(\\d{8})',\n  '(a)_(b)',\n]\n", "shard_patterns[1]", "found 2", 3},
		{"invalid RE2", "shard_patterns = ['orders_(\\d+']\n", "shard_patterns[0]", "not a valid RE2", 1},
		{"lookahead is not RE2", "\n\nshard_patterns = ['a(?=b)(c)']\n", "shard_patterns[0]", "not a valid RE2", 3},
		{"escaped in a basic string", "location = \"EU\"\nshard_patterns = [\"orders_\\\\d+\"]\n", "shard_patterns[0]", "capturing group", 2},
	}
	for _, tc := range cases {
		es := parseErrs(t, tc.src, LoadOptions{})
		if len(es) != 1 || es[0].Key != tc.key || es[0].Line != tc.line || !strings.Contains(es[0].Msg, tc.msg) {
			t.Errorf("%s: got %v, want %s at line %d containing %q", tc.name, es, tc.key, tc.line, tc.msg)
		}
	}
	if es := parseErrs(t, "shard_patterns = [1]\n", LoadOptions{}); len(es) != 1 {
		t.Fatalf("a non-string pattern is an error: %v", es)
	}
}

func TestShardPatternsAreLive(t *testing.T) {
	a := mustParse(t, "", LoadOptions{})
	b := mustParse(t, "shard_patterns = ['.+(\\d{8})']\n", LoadOptions{})
	if got := Diff(a, b); len(got) != 1 || got[0] != "shard_patterns" || restartKeys["shard_patterns"] {
		t.Fatalf("Diff = %v", got)
	}
}

func TestTUIFamilyRows(t *testing.T) {
	if c := mustParse(t, "", LoadOptions{}); c.FamilyRows() != DefaultFamilyRows || DefaultFamilyRows != 20 {
		t.Fatalf("default = %d", c.FamilyRows())
	}
	if c := mustParse(t, "[tui]\nfamily_rows = 0\n", LoadOptions{}); c.FamilyRows() != 0 {
		t.Fatalf("0 shows only the more row: %d", c.FamilyRows())
	}
	if c := mustParse(t, "[tui]\nfamily_rows = 50\n", LoadOptions{}); c.FamilyRows() != 50 {
		t.Fatalf("%d", c.FamilyRows())
	}
	for _, bad := range []string{"-1", `"20"`, "2.5"} {
		es := parseErrs(t, "location = \"EU\"\n[tui]\nfamily_rows = "+bad+"\n", LoadOptions{})
		if len(es) != 1 || es[0].Key != "tui.family_rows" || es[0].Line != 3 {
			t.Errorf("family_rows = %s: %v", bad, es)
		}
	}
}

// The template's commented examples must parse once uncommented, so that a
// user can enable them by deleting the "# ".
func TestTemplateExamplesParseUncommented(t *testing.T) {
	var b strings.Builder
	inPatterns := false
	for _, l := range strings.Split(Template(), "\n") {
		switch {
		case strings.HasPrefix(l, "# shard_patterns"):
			inPatterns = true
			l = strings.TrimPrefix(l, "# ")
		case inPatterns:
			inPatterns = l != "# ]"
			l = strings.TrimPrefix(l, "# ")
		case strings.HasPrefix(l, "# [tui]"), strings.HasPrefix(l, "# editing"), strings.HasPrefix(l, "# family_rows"):
			l = strings.TrimPrefix(l, "# ")
		}
		b.WriteString(l + "\n")
	}
	c, err := Parse([]byte(b.String()), "t", LoadOptions{})
	if err != nil {
		t.Fatalf("%v\n%s", err, b.String())
	}
	if len(c.Shards().Sources()) != 4 || c.FamilyRows() != 20 {
		t.Fatalf("%q %d", c.Shards().Sources(), c.FamilyRows())
	}
	// the examples do what their comments say
	for _, name := range []string{"events_20240101", "orders_0123456789abcdef01234567", "orders_0123456789abcdef01234567_history", "sessions_123e4567-e89b-12d3-a456-426614174000", "customer_123"} {
		if !c.Shards().MatchesAny(name) {
			t.Errorf("no template example matches %s", name)
		}
	}
}
