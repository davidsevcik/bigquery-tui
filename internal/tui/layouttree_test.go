package tui

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// tr parses the compact tree notation that layoutNode.String prints:
// row[...] and col[...] splits, E S Q R for the Panes, (S) for a hidden one,
// and :n after a child for its weight.
func tr(t *testing.T, s string) layoutNode {
	t.Helper()
	p := &treeParser{s: s}
	n, ok := p.node()
	if !ok || p.i != len(s) {
		t.Fatalf("bad tree %q at %d", s, p.i)
	}
	return n
}

type treeParser struct {
	s string
	i int
}

func (p *treeParser) node() (layoutNode, bool) {
	s := p.s[p.i:]
	for _, l := range []struct {
		pfx string
		z   Zone
	}{{"E", ZoneExplorer}, {"S", ZoneSchema}, {"Q", ZoneEditor}, {"R", ZoneResults}} {
		if strings.HasPrefix(s, l.pfx) {
			p.i++
			return leaf(l.z), true
		}
		if strings.HasPrefix(s, "("+l.pfx+")") {
			p.i += 3
			n := leaf(l.z)
			n.hidden = true
			return n, true
		}
	}
	var n layoutNode
	switch {
	case strings.HasPrefix(s, "row["):
		n.dir = splitRow
	case strings.HasPrefix(s, "col["):
		n.dir = splitCol
	default:
		return n, false
	}
	p.i += 4
	for p.i < len(p.s) && p.s[p.i] != ']' {
		if p.s[p.i] == ' ' {
			p.i++
		}
		k, ok := p.node()
		if !ok {
			return n, false
		}
		wt := 1
		if p.i < len(p.s) && p.s[p.i] == ':' {
			j := p.i + 1
			for j < len(p.s) && p.s[j] >= '0' && p.s[j] <= '9' {
				j++
			}
			wt, _ = strconv.Atoi(p.s[p.i+1 : j])
			p.i = j
		}
		n.kids = append(n.kids, k)
		n.weights = append(n.weights, wt)
	}
	if p.i >= len(p.s) {
		return n, false
	}
	p.i++
	return n, true
}

const classicStr = "row[col[E:50 S:50]:35 col[Q:50 R:50]:65]"

func TestPresetTrees(t *testing.T) {
	for name, want := range map[string]string{
		"classic": classicStr,
		"query":   "row[col[E:60 (S):40]:25 col[Q:50 R:50]:75]",
		"browse":  "col[row[E:55 S:45]:60 Q:15 R:25]",
		"tall":    "col[row[E:50 S:50]:30 Q:25 R:45]",
		"focus":   classicStr,
	} {
		if got := presetTree(name).String(); got != want {
			t.Errorf("%s: %s, want %s", name, got, want)
		}
	}
	if got := tr(t, classicStr).String(); got != classicStr {
		t.Fatalf("the notation round-trips: %s", got)
	}
}

func TestNextLayoutCyclesThePresets(t *testing.T) {
	for from, want := range map[string]string{"classic": "query", "query": "browse", "browse": "tall", "tall": "focus", "focus": "classic", "nonsense": "classic"} {
		if got := nextPreset(from); got != want {
			t.Errorf("after %s: %s, want %s", from, got, want)
		}
	}
}

func TestMoveToTheEdge(t *testing.T) {
	browse := presetTree("browse").String()
	tall := presetTree("tall").String()
	cases := []struct {
		tree string
		pane Zone
		d    direction
		want string // "" for unchanged
	}{
		{classicStr, ZoneExplorer, dirLeft, "row[E:33 S:23 col[Q:50 R:50]:43]"},
		{classicStr, ZoneExplorer, dirRight, "row[S:23 col[Q:50 R:50]:43 E:33]"},
		{classicStr, ZoneExplorer, dirUp, "col[E:33 row[S:35 col[Q:50 R:50]:65]:67]"},
		{classicStr, ZoneExplorer, dirDown, "col[row[S:35 col[Q:50 R:50]:65]:67 E:33]"},
		{classicStr, ZoneSchema, dirUp, "col[S:33 row[E:35 col[Q:50 R:50]:65]:67]"},
		{classicStr, ZoneEditor, dirLeft, "row[Q:33 col[E:50 S:50]:23 R:43]"},
		{classicStr, ZoneResults, dirRight, "row[col[E:50 S:50]:23 Q:43 R:33]"},
		{classicStr, ZoneResults, dirDown, "col[row[col[E:50 S:50]:35 Q:65]:67 R:33]"},
		{"row[E:33 S:23 col[Q:50 R:50]:43]", ZoneExplorer, dirLeft, ""},
		{browse, ZoneEditor, dirLeft, "row[Q:33 col[row[E:55 S:45]:71 R:29]:67]"},
		{browse, ZoneExplorer, dirDown, "col[S:40 Q:10 R:17 E:33]"},
		{browse, ZoneResults, dirDown, ""},
		{browse, ZoneSchema, dirRight, "row[col[E:60 Q:15 R:25]:67 S:33]"},
		{tall, ZoneResults, dirUp, "col[R:33 row[E:50 S:50]:36 Q:30]"},
		{tall, ZoneExplorer, dirLeft, "row[E:33 col[S:30 Q:25 R:45]:67]"},
		{tall, ZoneResults, dirDown, ""},
		// a split left with one child collapses, and a child split with its
		// parent's direction merges into it
		{"row[col[E:50 row[S:50 Q:50]:50]:50 R:50]", ZoneExplorer, dirDown, "col[row[S:25 Q:25 R:50]:67 E:33]"},
	}
	for _, c := range cases {
		got, why := moveToEdge(tr(t, c.tree), c.pane, c.d)
		want := c.want
		if want == "" {
			want = c.tree
			if why == "" {
				t.Errorf("%s %s %d: no reason given for doing nothing", c.tree, c.pane, c.d)
			}
		} else if why != "" {
			t.Errorf("%s %s %d: refused: %s", c.tree, c.pane, c.d, why)
		}
		if got.String() != want {
			t.Errorf("%s: %s to %d gives %s, want %s", c.tree, c.pane, c.d, got, want)
		}
	}
}

func TestSwapAndRotate(t *testing.T) {
	cases := []struct {
		op   string
		tree string
		pane Zone
		want string
		why  string
	}{
		{"x", classicStr, ZoneExplorer, "row[col[S:50 E:50]:35 col[Q:50 R:50]:65]", ""},
		{"x", classicStr, ZoneSchema, "row[col[S:50 E:50]:35 col[Q:50 R:50]:65]", ""}, // the last swaps with the previous
		{"x", "row[col[E:60 S:40]:25 Q:75]", ZoneExplorer, "row[col[S:60 E:40]:25 Q:75]", ""},
		{"x", "row[E:33 S:23 col[Q:50 R:50]:43]", ZoneSchema, "row[E:33 col[Q:50 R:50]:23 S:43]", ""}, // a subtree sibling
		{"x", "row[E:20 (S):30 Q:50]", ZoneExplorer, "row[Q:20 (S):30 E:50]", ""},                     // skips a hidden sibling
		{"x", "row[col[E:60 (S):40]:25 col[Q:50 R:50]:75]", ZoneExplorer, "", "nothing to swap with"},
		{"r", "row[E:20 S:30 Q:50]", ZoneExplorer, "row[Q:20 E:30 S:50]", ""},
		{"r", "row[E:20 (S):30 Q:40 R:10]", ZoneEditor, "row[R:20 (S):30 E:40 Q:10]", ""}, // hidden children keep their places
		{"r", "row[col[E:60 (S):40]:25 col[Q:50 R:50]:75]", ZoneExplorer, "", "nothing to rotate"},
	}
	for _, c := range cases {
		in := tr(t, c.tree)
		var got layoutNode
		var why string
		if c.op == "x" {
			got, why = swapPane(in, c.pane)
		} else {
			got, why = rotatePanes(in, c.pane)
		}
		want := c.want
		if want == "" {
			want = c.tree
		}
		if why != c.why || got.String() != want {
			t.Errorf("%s on %s in %s: %s %q, want %s %q", c.op, c.pane, c.tree, got, why, want, c.why)
		}
	}
}

func TestResize(t *testing.T) {
	cases := []struct {
		name string
		tree string
		pane Zone
		dir  splitDir
		grow bool
		w, h int
		want string
		why  string
	}{
		{"wider by 5%", classicStr, ZoneExplorer, splitRow, true, 120, 34, "row[col[E:50 S:50]:40 col[Q:50 R:50]:60]", ""},
		{"the last child trades with the previous one", classicStr, ZoneEditor, splitRow, false, 120, 34, "row[col[E:50 S:50]:40 col[Q:50 R:50]:60]", ""},
		{"taller", classicStr, ZoneExplorer, splitCol, true, 120, 34, "row[col[E:55 S:45]:35 col[Q:50 R:50]:65]", ""},
		{"shorter, last child", classicStr, ZoneSchema, splitCol, false, 120, 34, "row[col[E:55 S:45]:35 col[Q:50 R:50]:65]", ""},
		{"down to the Schema's minimum width", classicStr, ZoneExplorer, splitRow, false, 100, 34, "row[col[E:50 S:50]:30 col[Q:50 R:50]:70]", ""},
		{"at the minimum", "row[col[E:50 S:50]:30 col[Q:50 R:50]:70]", ZoneExplorer, splitRow, false, 100, 34, "", "Schema is at its minimum size"},
		{"clamped at the minimum", "row[col[E:50 S:50]:32 col[Q:50 R:50]:68]", ZoneSchema, splitRow, false, 100, 34, "row[col[E:50 S:50]:30 col[Q:50 R:50]:70]", ""},
		{"growing into a pane at its minimum", "row[col[E:50 S:50]:30 col[Q:50 R:50]:70]", ZoneEditor, splitRow, true, 100, 34, "", "Schema is at its minimum size"},
		{"the 10% floor", "row[E:12 Q:88]", ZoneExplorer, splitRow, false, 400, 34, "row[E:10 Q:90]", ""},
		{"at least one cell", "row[col[E:50 S:50]:35 Q:65]", ZoneExplorer, splitCol, true, 120, 12, "row[col[E:58 S:42]:35 Q:65]", ""},
		{"no pane beside", presetTree("tall").String(), ZoneEditor, splitRow, true, 90, 38, "", "Editor has no pane beside it"},
		{"no pane above or below", "row[E:50 Q:50]", ZoneExplorer, splitCol, true, 120, 34, "", "Explorer has no pane above or below it"},
		{"hidden siblings are skipped", "row[col[E:60 (S):40]:25 col[Q:50 R:50]:75]", ZoneExplorer, splitCol, true, 120, 34, "", "Explorer has no pane above or below it"},
		{"a hidden sibling is passed over", "row[E:40 (S):20 Q:40]", ZoneExplorer, splitRow, true, 120, 34, "row[E:45 (S):20 Q:35]", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := tr(t, c.tree)
			got, why := resizePane(in, c.pane, c.dir, c.grow, func(n layoutNode) map[Zone]rect {
				return rawRects(n, rect{0, 0, c.w, c.h}, nil)
			})
			want := c.want
			if want == "" {
				want = c.tree
			}
			if why != c.why || got.String() != want {
				t.Fatalf("%s %q, want %s %q", got, why, want, c.why)
			}
		})
	}
}

func TestEqualiseHideAndShow(t *testing.T) {
	if got := equalise(tr(t, "row[E:33 S:23 col[Q:70 R:30]:43]")).String(); got != "row[E:33 S:33 col[Q:50 R:50]:33]" {
		t.Errorf("equalise: %s", got)
	}
	hid, why := hidePane(presetTree("classic"), ZoneSchema)
	if why != "" || hid.String() != "row[col[E:50 (S):50]:35 col[Q:50 R:50]:65]" {
		t.Errorf("hide: %s %q", hid, why)
	}
	if got := showPane(hid, ZoneSchema).String(); got != classicStr {
		t.Errorf("show puts it back in its slot: %s", got)
	}
	last := tr(t, "row[col[(E):50 (S):50]:35 col[(Q):50 R:50]:65]")
	if got, why := hidePane(last, ZoneResults); why != "the last visible pane cannot be hidden" || got.String() != last.String() {
		t.Errorf("the last visible pane: %s %q", got, why)
	}
}

func TestCustomIsComputedByComparingTrees(t *testing.T) {
	classic := presetTree("classic")
	if isCustom("classic", classic) {
		t.Fatal("the preset itself is not custom")
	}
	swapped, _ := swapPane(classic, ZoneExplorer)
	if !isCustom("classic", swapped) {
		t.Fatal("a swap makes it custom")
	}
	back, _ := swapPane(swapped, ZoneExplorer)
	if isCustom("classic", back) {
		t.Fatalf("swapping back is the preset again: %s", back)
	}
	hid, _ := hidePane(presetTree("query"), ZoneExplorer)
	if !isCustom("query", hid) || isCustom("query", showPane(hid, ZoneExplorer)) {
		t.Fatal("hide and show")
	}
	if !isCustom("query", showPane(presetTree("query"), ZoneSchema)) {
		t.Fatal("showing query's Schema is custom")
	}
	lay := func(n layoutNode) map[Zone]rect { return rawRects(n, rect{0, 0, 120, 34}, nil) }
	wider, _ := resizePane(classic, ZoneExplorer, splitRow, true, lay)
	narrower, _ := resizePane(wider, ZoneExplorer, splitRow, false, lay)
	if !isCustom("classic", wider) || isCustom("classic", narrower) {
		t.Fatalf("resize and back: %s %s", wider, narrower)
	}
	// weights compare by proportion
	if isCustom("classic", tr(t, "row[col[E:1 S:1]:7 col[Q:3 R:3]:13]")) {
		t.Fatal("proportional weights are the same tree")
	}
}

func TestTreeJSON(t *testing.T) {
	in := tr(t, "row[col[E:60 (S):40]:25 col[Q:50 R:50]:75]")
	b, err := json.Marshal(treeToJSON(in))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"split":"row","weights":[25,75],"kids":[{"split":"col","weights":[60,40],"kids":[{"pane":"explorer"},{"pane":"schema","hidden":true}]},{"split":"col","weights":[50,50],"kids":[{"pane":"editor"},{"pane":"results"}]}]}`
	if string(b) != want {
		t.Fatalf("json %s", b)
	}
	var j treeJSON
	if err := json.Unmarshal(b, &j); err != nil {
		t.Fatal(err)
	}
	got, ok := treeFromJSON(&j)
	if !ok || got.String() != in.String() {
		t.Fatalf("round trip: %s %v", got, ok)
	}
	for _, bad := range []string{
		`{"pane":"explorer"}`, // the other Panes are missing
		`{"split":"row","weights":[1,1],"kids":[{"pane":"explorer"},{"pane":"explorer"},{"pane":"editor"},{"pane":"results"}]}`,
		`{"split":"row","weights":[1,1,1,1],"kids":[{"pane":"explorer"},{"pane":"schema"},{"pane":"editor"},{"pane":"chart"}]}`,
		`{"split":"diagonal","weights":[1,1,1,1],"kids":[{"pane":"explorer"},{"pane":"schema"},{"pane":"editor"},{"pane":"results"}]}`,
		`{"split":"row","weights":[1,0,1,1],"kids":[{"pane":"explorer"},{"pane":"schema"},{"pane":"editor"},{"pane":"results"}]}`,
		`{"split":"row","weights":[1,1],"kids":[{"pane":"explorer"},{"pane":"schema"},{"pane":"editor"},{"pane":"results"}]}`,
		`{"split":"row","weights":[1,1,1,1],"kids":[{"pane":"explorer","hidden":true},{"pane":"schema","hidden":true},{"pane":"editor","hidden":true},{"pane":"results","hidden":true}]}`,
	} {
		var j treeJSON
		if json.Unmarshal([]byte(bad), &j) != nil {
			continue
		}
		if got, ok := treeFromJSON(&j); ok {
			t.Errorf("%s is accepted: %s", bad, got)
		}
	}
}

// Codex review of phase 3.

func visibleShare(n layoutNode, z Zone) (num, den int) {
	for i, k := range n.kids {
		if !k.visible() {
			continue
		}
		den += n.weights[i]
		if k.isLeaf() && k.pane == z {
			num = n.weights[i]
		}
	}
	return
}

func TestRepeatedMovesKeepTheTreeLoadable(t *testing.T) {
	tree, _ := moveToEdge(presetTree("tall"), ZoneExplorer, dirDown)
	for i := range 14 {
		var why string
		if tree, why = moveToEdge(tree, tree.kids[0].pane, dirDown); why != "" {
			t.Fatal(why)
		}
		b, err := json.Marshal(treeToJSON(tree))
		if err != nil {
			t.Fatal(err)
		}
		var j treeJSON
		if err := json.Unmarshal(b, &j); err != nil {
			t.Fatal(err)
		}
		if _, ok := treeFromJSON(&j); !ok {
			t.Fatalf("after %d moves the saved tree no longer loads: %s", i+1, tree)
		}
	}
}

func TestMoveToTheEdgeSizesByTheVisibleSiblings(t *testing.T) {
	tree, why := moveToEdge(presetTree("query"), ZoneExplorer, dirLeft)
	if why != "" {
		t.Fatal(why)
	}
	if num, den := visibleShare(tree, ZoneExplorer); num*3 != den {
		t.Fatalf("the moved Pane gets %d/%d of the visible width, want a third: %s", num, den, tree)
	}
}
