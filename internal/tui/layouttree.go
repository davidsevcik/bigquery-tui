package tui

import (
	"fmt"
	"slices"
	"strings"
)

type presetDef struct {
	name        string
	tree        layoutNode
	schemaBelow int
	shareBelow  int
}

var presets = []presetDef{
	{presetClassic, classicTree, 100, 0},
	{presetQuery, queryTree, 0, 0},
	{presetBrowse, browseTree, 100, 28},
	{presetTall, tallTree, 0, 0},
	{presetFocus, classicTree, 0, 0},
}

var queryTree = split(splitRow, []int{25, 75},
	split(splitCol, []int{60, 40}, leaf(ZoneExplorer), hiddenLeaf(ZoneSchema)),
	split(splitCol, []int{50, 50}, leaf(ZoneEditor), leaf(ZoneResults)),
)

var browseTree = split(splitCol, []int{60, 15, 25},
	split(splitRow, []int{55, 45}, leaf(ZoneExplorer), leaf(ZoneSchema)),
	leaf(ZoneEditor), leaf(ZoneResults),
)

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

func presetTree(name string) layoutNode { return presetOf(name).tree.clone() }

func nextPreset(name string) string {
	for i, p := range presets {
		if p.name == name {
			return presets[(i+1)%len(presets)].name
		}
	}
	return presetClassic
}

func isCustom(preset string, t layoutNode) bool { return !sameTree(presetOf(preset).tree, t) }

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

func normalise(n layoutNode) layoutNode {
	if n.isLeaf() {
		return n
	}
	var kids []layoutNode
	var weights []int
	own := slices.Clone(n.weights)
	for i, k := range n.kids {
		k = normalise(k)
		if k.isLeaf() || k.dir != n.dir {
			kids = append(kids, k)
			weights = append(weights, own[i])
			continue
		}
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

func (n *layoutNode) at(p []int) *layoutNode {
	for _, i := range p {
		n = &n.kids[i]
	}
	return n
}

func (n layoutNode) visible() bool {
	if n.isLeaf() {
		return !n.hidden
	}
	return slices.ContainsFunc(n.kids, layoutNode.visible)
}

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

func showPane(t layoutNode, z Zone) layoutNode {
	out := t.clone()
	if p, ok := out.path(z); ok {
		out.at(p).hidden = false
	}
	return out
}

func resizePane(t layoutNode, z Zone, dir splitDir, grow bool, lay func(layoutNode) map[Zone]rect) (layoutNode, string) {
	p, ok := t.path(z)
	if !ok {
		return t, ""
	}
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
			break
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
