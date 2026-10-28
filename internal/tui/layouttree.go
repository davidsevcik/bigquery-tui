package tui

import (
	"fmt"
	"slices"
	"strings"
)

// The Layout's tree operations: the presets, and the ctrl+w window commands
// as pure functions. Each takes a tree and the focused Pane and returns the
// new tree, or the tree unchanged with the reason for the toast. Every result
// is normalised, so equal layouts are equal trees (see isCustom).

// presetDef is a named Layout. Its step-down rules apply to the edits of it
// too (a custom Layout steps down as its source preset does).
type presetDef struct {
	name string
	tree layoutNode
	// the Schema is left out below this many columns (0: only when the
	// Panes do not fit)
	schemaBelow int
	// below this many body rows the Editor and Results share one slot (0:
	// never)
	shareBelow int
}

// presets in the ctrl+w space order.
var presets = []presetDef{
	{presetClassic, classicTree, 100, 0},
	{presetQuery, queryTree, 0, 0},
	// below 30 terminal rows: the body leaves out the status and footer lines
	{presetBrowse, browseTree, 100, 28},
	{presetTall, tallTree, 0, 0},
	{presetFocus, classicTree, 0, 0},
}

// queryTree is a narrow Explorer beside the Editor over the Results; the
// Schema keeps its slot under the Explorer, hidden.
var queryTree = split(splitRow, []int{25, 75},
	split(splitCol, []int{60, 40}, leaf(ZoneExplorer), hiddenLeaf(ZoneSchema)),
	split(splitCol, []int{50, 50}, leaf(ZoneEditor), leaf(ZoneResults)),
)

// browseTree is the Explorer beside the Schema on top, the Editor and the
// Results below.
var browseTree = split(splitCol, []int{60, 15, 25},
	split(splitRow, []int{55, 45}, leaf(ZoneExplorer), leaf(ZoneSchema)),
	leaf(ZoneEditor), leaf(ZoneResults),
)

// tallTree is one column: the Explorer beside the Schema, then the Editor,
// then the Results.
var tallTree = split(splitCol, []int{30, 25, 45},
	split(splitRow, []int{50, 50}, leaf(ZoneExplorer), leaf(ZoneSchema)),
	leaf(ZoneEditor), leaf(ZoneResults),
)

func hiddenLeaf(z Zone) layoutNode {
	n := leaf(z)
	n.hidden = true
	return n
}

func presetOf(name string) presetDef {
	for _, p := range presets {
		if p.name == name {
			return p
		}
	}
	return presets[0]
}

// presetTree is a copy of the preset's tree.
func presetTree(name string) layoutNode { return presetOf(name).tree.clone() }

// nextPreset is the preset after name in the ctrl+w space order.
func nextPreset(name string) string {
	for i, p := range presets {
		if p.name == name {
			return presets[(i+1)%len(presets)].name
		}
	}
	return presetClassic
}

// isCustom reports whether the tree differs from its source preset's tree.
func isCustom(preset string, t layoutNode) bool { return !sameTree(presetOf(preset).tree, t) }

// sameTree compares structure, order, hidden flags and weights; weights
// compare by proportion.
func sameTree(a, b layoutNode) bool {
	if a.isLeaf() || b.isLeaf() {
		return a.pane == b.pane && a.hidden == b.hidden
	}
	if a.dir != b.dir || len(a.kids) != len(b.kids) {
		return false
	}
	sa, sb := sum(a.weights), sum(b.weights)
	for i := range a.kids {
		if a.weights[i]*sb != b.weights[i]*sa || !sameTree(a.kids[i], b.kids[i]) {
			return false
		}
	}
	return true
}

func (n layoutNode) isLeaf() bool { return n.pane != "" }

func (n layoutNode) clone() layoutNode {
	c := n
	c.weights = slices.Clone(n.weights)
	c.kids = make([]layoutNode, len(n.kids))
	for i, k := range n.kids {
		c.kids[i] = k.clone()
	}
	if n.kids == nil {
		c.kids = nil
	}
	return c
}

func sum(ws []int) int {
	s := 0
	for _, w := range ws {
		s += w
	}
	return s
}

var paneLetter = map[Zone]string{ZoneExplorer: "E", ZoneSchema: "S", ZoneEditor: "Q", ZoneResults: "R"}

// String is the compact form used in tests and messages: row[...] and
// col[...], E S Q R for the Panes, (S) when hidden, and each child's weight as
// a percentage of its split.
func (n layoutNode) String() string {
	if n.isLeaf() {
		if n.hidden {
			return "(" + paneLetter[n.pane] + ")"
		}
		return paneLetter[n.pane]
	}
	var sb strings.Builder
	sb.WriteString(map[splitDir]string{splitRow: "row[", splitCol: "col["}[n.dir])
	s := sum(n.weights)
	for i, k := range n.kids {
		if i > 0 {
			sb.WriteString(" ")
		}
		fmt.Fprintf(&sb, "%s:%d", k, (200*n.weights[i]+s)/(2*s))
	}
	sb.WriteString("]")
	return sb.String()
}

// normalise replaces a split left with one child by that child, and merges a
// child split with its parent's direction into the parent, its weights
// scaled to the share it had. Weights are kept small (divided by their
// common divisor).
func normalise(n layoutNode) layoutNode {
	if n.isLeaf() {
		return n
	}
	var kids []layoutNode
	var weights []int
	own := slices.Clone(n.weights) // the weights of the children still to come
	for i, k := range n.kids {
		k = normalise(k)
		if k.isLeaf() || k.dir != n.dir {
			kids = append(kids, k)
			weights = append(weights, own[i])
			continue
		}
		// k's children take k's share: scale everything else to keep exact
		// proportions
		ks := sum(k.weights)
		for j := range weights {
			weights[j] *= ks
		}
		for j := i + 1; j < len(own); j++ {
			own[j] *= ks
		}
		for j, kk := range k.kids {
			kids = append(kids, kk)
			weights = append(weights, own[i]*k.weights[j])
		}
	}
	if len(kids) == 1 {
		return kids[0]
	}
	g := 0
	for _, w := range weights {
		g = gcd(g, w)
	}
	for i := range weights {
		weights[i] /= max(g, 1)
	}
	return layoutNode{dir: n.dir, weights: boundWeights(weights), kids: kids}
}

// maxWeightSum bounds a split's weights: repeated edits multiply them, and a
// saved tree with weights above the loader's limit would be dropped. Above
// it the weights are scaled down to sum to about 1000, keeping every child
// at least 1 (a share finer than 0.1% makes no difference on a terminal).
const maxWeightSum = 10000

func boundWeights(ws []int) []int {
	s := sum(ws)
	if s <= maxWeightSum {
		return ws
	}
	out := make([]int, len(ws))
	for i, w := range ws {
		out[i] = max(1, int((int64(w)*1000+int64(s)/2)/int64(s)))
	}
	return out
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// path is the way from the root to a Pane: the index of the child taken at
// each split.
func (n layoutNode) path(z Zone) ([]int, bool) {
	if n.isLeaf() {
		return nil, n.pane == z
	}
	for i, k := range n.kids {
		if p, ok := k.path(z); ok {
			return append([]int{i}, p...), true
		}
	}
	return nil, false
}

// at is the node at the path.
func (n *layoutNode) at(p []int) *layoutNode {
	for _, i := range p {
		n = &n.kids[i]
	}
	return n
}

// visible reports whether some Pane under n is not hidden.
func (n layoutNode) visible() bool {
	if n.isLeaf() {
		return !n.hidden
	}
	return slices.ContainsFunc(n.kids, layoutNode.visible)
}

// visiblePanes lists the Panes not hidden, in the cycle order.
func (n layoutNode) visiblePanes() []Zone {
	var out []Zone
	for _, z := range paneOrder {
		if p, ok := n.path(z); ok && !n.at(p).hidden {
			out = append(out, z)
		}
	}
	return out
}

func (n layoutNode) hiddenPane(z Zone) bool {
	p, ok := n.path(z)
	return ok && n.at(p).hidden
}

// remove takes the Pane's leaf out of the tree and normalises it.
func (n layoutNode) remove(z Zone) layoutNode {
	t := n.clone()
	p, ok := t.path(z)
	if !ok || len(p) == 0 {
		return t
	}
	parent := t.at(p[:len(p)-1])
	i := p[len(p)-1]
	parent.kids = slices.Delete(parent.kids, i, i+1)
	parent.weights = slices.Delete(parent.weights, i, i+1)
	return normalise(t)
}

// moveToEdge is ctrl+w H J K L: the Pane goes to the far left, bottom, top
// or right, spanning the whole height or width (Vim's CTRL-W H).
func moveToEdge(t layoutNode, z Zone, d direction) (layoutNode, string) {
	dir, first := splitRow, d == dirLeft
	if d == dirUp || d == dirDown {
		dir, first = splitCol, d == dirUp
	}
	end := 0
	if !first {
		end = len(t.kids) - 1
	}
	if !t.isLeaf() && t.dir == dir && t.kids[end].isLeaf() && t.kids[end].pane == z {
		return t, fmt.Sprintf("%s is already %s", z, map[direction]string{dirLeft: "at the far left", dirRight: "at the far right", dirUp: "at the top", dirDown: "at the bottom"}[d])
	}
	p, ok := t.path(z)
	if !ok {
		return t, ""
	}
	lf := *t.at(p)
	rest := t.remove(z)
	var out layoutNode
	if !rest.isLeaf() && rest.dir == dir {
		// a third of the split: half the sum of the others that take room
		// (hidden ones keep their slot but no space)
		ws := slices.Clone(rest.weights)
		s := 0
		for i, k := range rest.kids {
			if k.visible() {
				s += ws[i]
			}
		}
		for i := range ws {
			ws[i] *= 2
		}
		out = layoutNode{dir: dir, weights: ws, kids: slices.Clone(rest.kids)}
		if first {
			out.kids, out.weights = slices.Insert(out.kids, 0, lf), slices.Insert(out.weights, 0, s)
		} else {
			out.kids, out.weights = append(out.kids, lf), append(out.weights, s)
		}
	} else if first {
		out = split(dir, []int{1, 2}, lf, rest)
	} else {
		out = split(dir, []int{2, 1}, rest, lf)
	}
	return normalise(out), ""
}

// visibleSibling is the next visible child of parent after i, or the
// previous one when there is none after it.
func visibleSibling(parent *layoutNode, i int) (int, bool) {
	for j := i + 1; j < len(parent.kids); j++ {
		if parent.kids[j].visible() {
			return j, true
		}
	}
	for j := i - 1; j >= 0; j-- {
		if parent.kids[j].visible() {
			return j, true
		}
	}
	return 0, false
}

// swapPane is ctrl+w x: the Pane trades places with the next visible
// sibling in its split (the previous one when it is the last). The slots keep
// their sizes.
func swapPane(t layoutNode, z Zone) (layoutNode, string) {
	out := t.clone()
	p, ok := out.path(z)
	if !ok || len(p) == 0 {
		return t, "nothing to swap with"
	}
	parent, i := out.at(p[:len(p)-1]), p[len(p)-1]
	j, ok := visibleSibling(parent, i)
	if !ok {
		return t, "nothing to swap with"
	}
	parent.kids[i], parent.kids[j] = parent.kids[j], parent.kids[i]
	return normalise(out), ""
}

// rotatePanes is ctrl+w r: the visible children of the Pane's split move one
// place on, the last becoming the first. Hidden children and the slot sizes
// stay where they are.
func rotatePanes(t layoutNode, z Zone) (layoutNode, string) {
	out := t.clone()
	p, ok := out.path(z)
	if !ok || len(p) == 0 {
		return t, "nothing to rotate"
	}
	parent := out.at(p[:len(p)-1])
	var vis []int
	for i, k := range parent.kids {
		if k.visible() {
			vis = append(vis, i)
		}
	}
	if len(vis) < 2 {
		return t, "nothing to rotate"
	}
	old := slices.Clone(parent.kids)
	for k, i := range vis {
		parent.kids[i] = old[vis[(k+len(vis)-1)%len(vis)]]
	}
	return normalise(out), ""
}

// equalise is ctrl+w =: every split gives its children equal shares.
func equalise(t layoutNode) layoutNode {
	out := t.clone()
	var eq func(n *layoutNode)
	eq = func(n *layoutNode) {
		for i := range n.kids {
			n.weights[i] = 1
			eq(&n.kids[i])
		}
	}
	eq(&out)
	return out
}

// hidePane is ctrl+w c: the Pane keeps its slot, which takes no space. The
// last visible Pane cannot be hidden.
func hidePane(t layoutNode, z Zone) (layoutNode, string) {
	vis := t.visiblePanes()
	if !slices.Contains(vis, z) {
		return t, ""
	}
	if len(vis) == 1 {
		return t, "the last visible pane cannot be hidden"
	}
	out := t.clone()
	p, _ := out.path(z)
	out.at(p).hidden = true
	return out, ""
}

// showPane clears the Pane's hidden flag: it comes back in its slot.
func showPane(t layoutNode, z Zone) layoutNode {
	out := t.clone()
	if p, ok := out.path(z); ok {
		out.at(p).hidden = false
	}
	return out
}

// resizePane is ctrl+w < > - +: the Pane's share of the nearest split along
// dir (row for the width, col for the height) grows or shrinks by 5% of the
// split (at least one cell), traded with its next visible sibling (the
// previous one when it is the last). lay gives the rectangles at the current
// size, by weight; the step is clamped so that no Pane on the shrinking side
// goes below its minimum and no child below 10% of the split.
func resizePane(t layoutNode, z Zone, dir splitDir, grow bool, lay func(layoutNode) map[Zone]rect) (layoutNode, string) {
	p, ok := t.path(z)
	if !ok {
		return t, ""
	}
	// the nearest split along dir in which the Pane's child has a visible sibling
	d := len(p) - 1
	var i, j int
	for ; d >= 0; d-- {
		parent := t.at(p[:d])
		if parent.dir != dir {
			continue
		}
		if j, ok = visibleSibling(parent, p[d]); ok {
			i = p[d]
			break
		}
	}
	if d < 0 {
		if dir == splitRow {
			return t, string(z) + " has no pane beside it"
		}
		return t, string(z) + " has no pane above or below it"
	}
	out := t.clone()
	sp := out.at(p[:d])
	// enough resolution for a 5% step
	if s := sum(sp.weights); s < 1000 {
		f := (1000 + s - 1) / s
		for k := range sp.weights {
			sp.weights[k] *= f
		}
	}
	total := sum(sp.weights)
	before := lay(out)
	extent := func(n layoutNode, rs map[Zone]rect) int {
		lo, hi := 1<<30, -1
		for _, zz := range n.visiblePanes() {
			if r, ok := rs[zz]; ok {
				a, b := r.x, r.x+r.w
				if dir == splitCol {
					a, b = r.y, r.y+r.h
				}
				lo, hi = min(lo, a), max(hi, b)
			}
		}
		return max(0, hi-lo)
	}
	span := 0
	for _, k := range sp.kids {
		span += extent(k, before)
	}
	step := max((total*5+99)/100, (total+max(span, 1)-1)/max(span, 1))
	grower, shrinker := i, j
	if !grow {
		grower, shrinker = j, i
	}
	shrunkFrom := extent(sp.kids[shrinker], before)
	blocked := ""
	for s := step; s > 0; s-- {
		cand := out.clone()
		c := cand.at(p[:d])
		c.weights[grower] += s
		c.weights[shrinker] -= s
		if c.weights[shrinker]*10 < total {
			blocked = firstVisible(c.kids[shrinker])
			continue
		}
		rs := lay(cand)
		if why := belowMin(c.kids[shrinker], rs, dir); why != "" {
			blocked = why
			continue
		}
		if extent(c.kids[shrinker], rs) >= shrunkFrom {
			break // no longer a visible change
		}
		return normalise(cand), ""
	}
	if blocked == "" {
		blocked = firstVisible(t.at(p[:d]).kids[shrinker])
	}
	return t, blocked + " is at its minimum size"
}

func firstVisible(n layoutNode) string {
	if v := n.visiblePanes(); len(v) > 0 {
		return string(v[0])
	}
	return ""
}

// belowMin names the first Pane under n that is drawn smaller than its
// minimum along dir.
func belowMin(n layoutNode, rs map[Zone]rect, dir splitDir) string {
	for _, z := range n.visiblePanes() {
		r, ok := rs[z]
		if !ok {
			continue
		}
		if mn := paneMin[z]; (dir == splitRow && r.w < mn.w) || (dir == splitCol && r.h < mn.h) {
			return string(z)
		}
	}
	return ""
}

// --- the tree in state/tui.json

// treeJSON is a node of the saved tree: a Pane, or a split with its children
// and their weights. hidden is left out when false.
type treeJSON struct {
	Pane    string      `json:"pane,omitempty"`
	Hidden  bool        `json:"hidden,omitempty"`
	Split   string      `json:"split,omitempty"`
	Weights []int       `json:"weights,omitempty"`
	Kids    []*treeJSON `json:"kids,omitempty"`
}

var paneJSON = map[Zone]string{ZoneExplorer: "explorer", ZoneSchema: "schema", ZoneEditor: "editor", ZoneResults: "results"}

func treeToJSON(n layoutNode) *treeJSON {
	if n.isLeaf() {
		return &treeJSON{Pane: paneJSON[n.pane], Hidden: n.hidden}
	}
	j := &treeJSON{Split: map[splitDir]string{splitRow: "row", splitCol: "col"}[n.dir], Weights: slices.Clone(n.weights)}
	for _, k := range n.kids {
		j.Kids = append(j.Kids, treeToJSON(k))
	}
	return j
}

// treeFromJSON checks and reads a saved tree: each of the four Panes exactly
// once, positive weights, one per child, and at least one Pane visible.
func treeFromJSON(j *treeJSON) (layoutNode, bool) {
	seen := map[Zone]bool{}
	var read func(j *treeJSON) (layoutNode, bool)
	read = func(j *treeJSON) (layoutNode, bool) {
		if j == nil {
			return layoutNode{}, false
		}
		if j.Pane != "" {
			for z, name := range paneJSON {
				if name == j.Pane && !seen[z] && j.Split == "" && len(j.Kids) == 0 {
					seen[z] = true
					return layoutNode{pane: z, hidden: j.Hidden}, true
				}
			}
			return layoutNode{}, false
		}
		var n layoutNode
		switch j.Split {
		case "row":
			n.dir = splitRow
		case "col":
			n.dir = splitCol
		default:
			return n, false
		}
		if len(j.Kids) == 0 || len(j.Weights) != len(j.Kids) {
			return n, false
		}
		for i, kj := range j.Kids {
			k, ok := read(kj)
			if !ok || j.Weights[i] <= 0 || j.Weights[i] > 1<<20 {
				return n, false
			}
			n.kids = append(n.kids, k)
			n.weights = append(n.weights, j.Weights[i])
		}
		return n, true
	}
	n, ok := read(j)
	if !ok || len(seen) != len(paneOrder) || !n.visible() {
		return layoutNode{}, false
	}
	return normalise(n), true
}
