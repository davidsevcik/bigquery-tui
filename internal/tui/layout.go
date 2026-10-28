package tui

import "slices"

// The Layout arranges the four Panes (Explorer, Schema, Editor, Results) on
// screen: a split tree whose leaves are the Panes (ADR 0002). arrange turns
// it, the body size and the focus into a rectangle per Pane; drawing and key
// handling both use it. Nothing here reads the Model.

// rect is a Pane's place in the body, in cells.
type rect struct{ x, y, w, h int }

// splitDir is how a split lays out its children.
type splitDir int

const (
	splitRow splitDir = iota // side by side, separated by a one-column line
	splitCol                 // stacked; each Pane's title row is the divider
)

// layoutNode is one node of the split tree: a leaf holds a Pane, a split
// holds children with an integer weight each. A Pane appears at most once. A
// hidden leaf keeps its slot but takes no space.
type layoutNode struct {
	pane    Zone
	hidden  bool
	dir     splitDir
	weights []int
	kids    []layoutNode
}

func leaf(z Zone) layoutNode { return layoutNode{pane: z} }

func split(dir splitDir, weights []int, kids ...layoutNode) layoutNode {
	return layoutNode{dir: dir, weights: weights, kids: kids}
}

// classicTree is Explorer over Schema on the left, Editor over Results on the
// right. Both columns use the same weights so that their rows line up: the
// Explorer sits beside the Editor and the Schema beside the Results.
var classicTree = split(splitRow, []int{35, 65},
	split(splitCol, []int{50, 50}, leaf(ZoneExplorer), leaf(ZoneSchema)),
	split(splitCol, []int{50, 50}, leaf(ZoneEditor), leaf(ZoneResults)),
)

// Layout presets (layouttree.go has their trees). focus uses the classic tree
// for directions but draws only the focused Pane, under a one-line pane bar.
const (
	presetClassic = "classic"
	presetQuery   = "query"
	presetBrowse  = "browse"
	presetTall    = "tall"
	presetFocus   = "focus"
)

func knownPreset(name string) bool {
	for _, p := range presets {
		if p.name == name {
			return true
		}
	}
	return false
}

// paneOrder is the fixed cycle order of ctrl+o.
var paneOrder = []Zone{ZoneExplorer, ZoneSchema, ZoneEditor, ZoneResults}

// paneMin is the smallest size a Pane is drawn at, title row included.
var paneMin = map[Zone]struct{ w, h int }{
	ZoneExplorer: {20, 4},
	ZoneSchema:   {30, 5},
	ZoneEditor:   {20, 3},
	ZoneResults:  {20, 4},
}

// arrangement is where everything goes for one frame.
type arrangement struct {
	rects    map[Zone]rect // Panes drawn now, each exactly this size; they tile the body with seps (and bar)
	seps     []rect        // one-column vertical separators
	bar      *rect         // the pane bar row, nil otherwise
	barPanes []Zone        // the Panes the bar names; the one drawn is in brackets
	barHint  string
	nav      map[Zone]rect // rectangles used for ctrl+w h/j/k/l
	cycle    []Zone        // ctrl+o order: Explorer, Schema, Editor, Results minus Panes not reachable
	stepped  bool          // a small-terminal step-down applies
	single   bool          // one Pane at a time: the focus Layout or the small-terminal fallback
	zoom     Zone          // the Pane maximised with ctrl+w z ("" for none)
	// the focused Pane is shown maximised because the step-down left it out
	offLayout bool
}

// arrange lays the tree out in a w×h body. preset is the tree's source
// preset (its step-down rules apply), focused the Pane with focus, zoom the
// maximised Pane ("" for none) and mru the Panes, most recently focused
// first.
//
// Directions (nav) follow the Layout before the shared slot, the fallback and
// the zoom, so that ctrl+w h/j/k/l moves as on the full screen; a Pane the
// step-down leaves out is skipped, as ctrl+o skips it.
func arrange(preset string, tree layoutNode, focused, zoom Zone, mru []Zone, w, h int) arrangement {
	visible := tree.visiblePanes()
	a := arrangement{single: true}
	if preset != presetFocus {
		a = stepDown(presetOf(preset), tree, focused, mru, w, h)
	}
	if a.single {
		return arrangement{
			rects:    map[Zone]rect{focused: {0, 1, w, h - 1}},
			bar:      &rect{0, 0, w, 1},
			barPanes: visible,
			barHint:  "ctrl+o next",
			nav:      lay(tree, rect{0, 0, w, h}, nil).rects,
			cycle:    visible,
			stepped:  a.stepped,
			single:   true,
		}
	}
	// The focused Pane is always on screen: one the step-down left out is
	// shown maximised.
	hint := "ctrl+w z restore"
	if _, ok := a.rects[focused]; !ok && zoom == "" {
		zoom, hint, a.offLayout = focused, "ctrl+o next", true
	} else {
		a.zoom = zoom
	}
	if zoom != "" {
		a.barPanes = nil
		for _, z := range paneOrder {
			if z == zoom || slices.Contains(a.cycle, z) {
				a.barPanes = append(a.barPanes, z)
			}
		}
		a.rects, a.seps = map[Zone]rect{zoom: {0, 1, w, h - 1}}, nil
		a.bar, a.barHint = &rect{0, 0, w, 1}, hint
	}
	return a
}

// stepDown is the Layout as it fits the body. Below 70 columns, or when the
// Panes are still too small after the steps below, one Pane at a time is
// shown (single). Otherwise, in this order: the preset may leave out the
// Schema below a width; browse shares one slot between the Editor and the
// Results below a height (the one focused last is drawn, under a pane bar);
// and the Schema is left out when a Pane would be smaller than its minimum.
// The last visible Pane is never left out.
func stepDown(p presetDef, tree layoutNode, focused Zone, mru []Zone, w, h int) arrangement {
	if w < 70 {
		return arrangement{single: true, stepped: true}
	}
	body := rect{0, 0, w, h}
	visible := tree.visiblePanes()
	omit := map[Zone]bool{}
	canOmit := func(z Zone) bool {
		return !omit[z] && slices.Contains(visible, z) && len(visible)-len(omit) > 1
	}
	if p.schemaBelow > 0 && w < p.schemaBelow && canOmit(ZoneSchema) {
		omit[ZoneSchema] = true
	}
	for {
		a := lay(tree, body, omit)
		a.nav = a.rects
		for _, z := range visible {
			if !omit[z] {
				a.cycle = append(a.cycle, z)
			}
		}
		if p.shareBelow > 0 && h < p.shareBelow && slices.Contains(a.cycle, ZoneEditor) && slices.Contains(a.cycle, ZoneResults) {
			shown, other := ZoneEditor, ZoneResults
			for _, z := range append([]Zone{focused}, mru...) {
				if z == ZoneResults || z == ZoneEditor {
					shown = z
					break
				}
			}
			if shown == ZoneResults {
				other = ZoneEditor
			}
			s := lay(mergeInto(tree, other, shown), body, omit)
			r := s.rects[shown]
			a.rects, a.seps = s.rects, s.seps
			a.rects[shown] = rect{r.x, r.y + 1, r.w, r.h - 1}
			a.bar, a.barPanes, a.barHint = &rect{r.x, r.y, r.w, 1}, []Zone{ZoneEditor, ZoneResults}, "ctrl+w j/k switch"
		}
		a.stepped = len(omit) > 0 || a.bar != nil
		if a.fits() {
			return a
		}
		if !canOmit(ZoneSchema) {
			return arrangement{single: true, stepped: true}
		}
		omit[ZoneSchema] = true
	}
}

// mergeInto leaves the Pane gone out of the tree, its share going to into
// when the two are in the same split (they then share one slot).
func mergeInto(t layoutNode, gone, into Zone) layoutNode {
	out := t.clone()
	p, _ := out.path(gone)
	q, _ := out.path(into)
	if len(p) > 0 && len(q) == len(p) && slices.Equal(p[:len(p)-1], q[:len(q)-1]) {
		parent := out.at(p[:len(p)-1])
		parent.weights[q[len(q)-1]] += parent.weights[p[len(p)-1]]
	}
	out.at(p).hidden = true
	return out
}

// fits reports whether every drawn Pane meets its minimum size.
func (a arrangement) fits() bool {
	for z, r := range a.rects {
		if mn := paneMin[z]; r.w < mn.w || r.h < mn.h {
			return false
		}
	}
	return true
}

// lay lays the tree out in the body, leaving out hidden Panes and the Panes
// in omit. A child too small for its Panes' minimums takes the room from its
// siblings while they can spare it.
func lay(t layoutNode, body rect, omit map[Zone]bool) arrangement {
	a := arrangement{rects: map[Zone]rect{}}
	place(t, body, omit, true, &a)
	return a
}

// rawRects is lay by the weights alone, without the minimums (resizing is
// clamped on it, so that weights never drift where the screen would not
// change).
func rawRects(t layoutNode, body rect, omit map[Zone]bool) map[Zone]rect {
	a := arrangement{rects: map[Zone]rect{}}
	place(t, body, omit, false, &a)
	return a.rects
}

// place lays node out in r, leaving out hidden Panes and the Panes in omit,
// and appends the rectangles and separators to a. With fix, children below
// their minimum are grown at their siblings' expense.
func place(n layoutNode, r rect, omit map[Zone]bool, fix bool, a *arrangement) {
	if n.isLeaf() {
		a.rects[n.pane] = r
		return
	}
	var kids []layoutNode
	var weights []int
	for i, k := range n.kids {
		if !k.omitted(omit) {
			kids = append(kids, k)
			weights = append(weights, n.weights[i])
		}
	}
	if len(kids) == 0 {
		return
	}
	extent, seps := r.h, 0
	if n.dir == splitRow {
		extent, seps = r.w, len(kids)-1
	}
	// Each child gets its share of the whole extent; the last one takes what
	// is left after the separators.
	total := sum(weights)
	sizes := make([]int, len(kids))
	used := 0
	for i := range kids {
		sizes[i] = extent * weights[i] / total
		if i == len(kids)-1 {
			sizes[i] = extent - seps - used
		}
		used += sizes[i]
	}
	if fix {
		fixMinimums(kids, weights, sizes, extent-seps, n.dir, omit)
	}
	at := 0
	for i, k := range kids {
		if n.dir == splitRow {
			place(k, rect{r.x + at, r.y, sizes[i], r.h}, omit, fix, a)
			at += sizes[i]
			if i < len(kids)-1 {
				a.seps = append(a.seps, rect{r.x + at, r.y, 1, r.h})
				at++
			}
		} else {
			place(k, rect{r.x, r.y + at, r.w, sizes[i]}, omit, fix, a)
			at += sizes[i]
		}
	}
}

// fixMinimums gives each child below its minimum extent that minimum, and
// shares the rest among the others by weight, when the minimums fit at all.
func fixMinimums(kids []layoutNode, weights, sizes []int, avail int, dir splitDir, omit map[Zone]bool) {
	mins := make([]int, len(kids))
	need, short := 0, false
	for i, k := range kids {
		mins[i] = minExtent(k, dir, omit)
		need += mins[i]
		short = short || sizes[i] < mins[i]
	}
	if !short || need > avail {
		return
	}
	fixed := make([]bool, len(kids))
	for {
		changed := false
		for i := range kids {
			if !fixed[i] && sizes[i] < mins[i] {
				fixed[i], changed = true, true
			}
		}
		if !changed {
			return
		}
		rest, fw, last := avail, 0, -1
		for i := range kids {
			if fixed[i] {
				sizes[i] = mins[i]
				rest -= mins[i]
			} else {
				fw += weights[i]
				last = i
			}
		}
		if last < 0 {
			sizes[len(sizes)-1] += rest
			return
		}
		used := 0
		for i := range kids {
			if fixed[i] {
				continue
			}
			if i == last {
				sizes[i] = rest - used
			} else {
				sizes[i] = rest * weights[i] / fw
				used += sizes[i]
			}
		}
	}
}

// minExtent is the smallest width (along splitRow) or height (splitCol) the
// node's drawn Panes need.
func minExtent(n layoutNode, axis splitDir, omit map[Zone]bool) int {
	if n.isLeaf() {
		if axis == splitRow {
			return paneMin[n.pane].w
		}
		return paneMin[n.pane].h
	}
	total, count := 0, 0
	for _, k := range n.kids {
		if k.omitted(omit) {
			continue
		}
		m := minExtent(k, axis, omit)
		if n.dir == axis {
			total += m
			count++
		} else {
			total = max(total, m)
		}
	}
	if n.dir == axis && axis == splitRow && count > 1 {
		total += count - 1 // the separators
	}
	return total
}

// omitted reports whether every Pane under n is hidden or left out.
func (n layoutNode) omitted(omit map[Zone]bool) bool {
	if n.isLeaf() {
		return n.hidden || omit[n.pane]
	}
	for _, k := range n.kids {
		if !k.omitted(omit) {
			return false
		}
	}
	return true
}

// direction is a ctrl+w h/j/k/l move.
type direction int

const (
	dirLeft direction = iota
	dirDown
	dirUp
	dirRight
)

// neighbour is the Pane ctrl+w moves to from the Pane from: of the Panes that
// lie entirely on that side and overlap it on the other axis, the nearest;
// among those the most recently focused (mru, most recent first); failing
// that the largest overlap, then the earliest in the cycle order.
func neighbour(nav map[Zone]rect, from Zone, d direction, mru []Zone) (Zone, bool) {
	f, ok := nav[from]
	if !ok {
		return "", false
	}
	type cand struct {
		z            Zone
		gap, overlap int
	}
	var cands []cand
	for _, z := range paneOrder {
		c, ok := nav[z]
		if z == from || !ok {
			continue
		}
		var gap, overlap int
		switch d {
		case dirLeft:
			gap, overlap = f.x-(c.x+c.w), span(f.y, f.h, c.y, c.h)
		case dirRight:
			gap, overlap = c.x-(f.x+f.w), span(f.y, f.h, c.y, c.h)
		case dirUp:
			gap, overlap = f.y-(c.y+c.h), span(f.x, f.w, c.x, c.w)
		case dirDown:
			gap, overlap = c.y-(f.y+f.h), span(f.x, f.w, c.x, c.w)
		}
		if gap >= 0 && overlap > 0 {
			cands = append(cands, cand{z, gap, overlap})
		}
	}
	if len(cands) == 0 {
		return "", false
	}
	nearest := cands[0].gap
	for _, c := range cands {
		nearest = min(nearest, c.gap)
	}
	var near []cand
	for _, c := range cands {
		if c.gap == nearest {
			near = append(near, c)
		}
	}
	for _, z := range mru {
		for _, c := range near {
			if c.z == z {
				return z, true
			}
		}
	}
	best := near[0] // near keeps the cycle order
	for _, c := range near[1:] {
		if c.overlap > best.overlap {
			best = c
		}
	}
	return best.z, true
}

// span is how far the ranges [a, a+al) and [b, b+bl) overlap.
func span(a, al, b, bl int) int {
	return min(a+al, b+bl) - max(a, b)
}
