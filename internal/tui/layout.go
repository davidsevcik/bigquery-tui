package tui

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
// holds children with an integer weight each. A Pane appears at most once.
type layoutNode struct {
	pane    Zone
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

// Layout presets. focus uses the classic tree for directions but draws only
// the focused Pane, under a one-line pane bar.
const (
	presetClassic = "classic"
	presetFocus   = "focus"
)

var presetNames = []string{presetClassic, presetFocus}

func knownPreset(name string) bool {
	for _, n := range presetNames {
		if n == name {
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
	rects   map[Zone]rect // Panes drawn now, each exactly this size; they tile the body with seps (and bar)
	seps    []rect        // one-column vertical separators
	bar     *rect         // the focus pane bar row, nil otherwise
	nav     map[Zone]rect // rectangles used for ctrl+w h/j/k/l
	cycle   []Zone        // ctrl+o order: Explorer, Schema, Editor, Results minus Panes not reachable
	stepped bool          // a small-terminal step-down applies
}

// arrange lays the preset out in a w×h body. focused is the Pane with focus,
// maxed the maximised Pane ("" for none).
func arrange(preset string, focused, maxed Zone, w, h int) arrangement {
	full := classic(w, h, nil)
	a := full
	single := preset == presetFocus // one Pane at a time
	if !single {
		a, single = stepDown(w, h)
	}
	a.cycle = paneOrder
	if single {
		a = arrangement{
			rects:   map[Zone]rect{focused: {0, 1, w, h - 1}},
			bar:     &rect{0, 0, w, 1},
			cycle:   paneOrder,
			stepped: a.stepped,
		}
	} else if a.stepped {
		a.cycle = nil
		for _, z := range paneOrder {
			if _, ok := a.rects[z]; ok {
				a.cycle = append(a.cycle, z)
			}
		}
	}
	a.nav = a.rects
	// The focused Pane is always on screen: one the step-down left out is
	// shown maximised.
	if _, ok := a.rects[focused]; !ok && maxed == "" {
		maxed = focused
	}
	if maxed != "" {
		a.rects, a.seps, a.bar = map[Zone]rect{maxed: {0, 0, w, h}}, nil, nil
	}
	// While one Pane at a time is drawn, directions still follow the classic tree.
	if len(a.rects) == 1 {
		a.nav = full.rects
	}
	return a
}

// stepDown is classic as it fits the body: below 100 columns, or when a Pane
// would be smaller than its minimum, the Schema is left out; below 70 columns,
// or when that is still too small, one Pane at a time is shown (single).
func stepDown(w, h int) (a arrangement, single bool) {
	if w < 70 {
		return arrangement{stepped: true}, true
	}
	omit := map[Zone]bool{}
	if w < 100 {
		omit[ZoneSchema] = true
	}
	for {
		a = classic(w, h, omit)
		a.stepped = len(omit) > 0
		if a.fits() {
			return a, false
		}
		if omit[ZoneSchema] {
			return arrangement{stepped: true}, true
		}
		omit[ZoneSchema] = true
	}
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

// classic lays the classic tree out in the body, leaving out the Panes in omit.
func classic(w, h int, omit map[Zone]bool) arrangement {
	a := arrangement{rects: map[Zone]rect{}}
	place(classicTree, rect{0, 0, w, h}, omit, &a)
	return a
}

// place lays node out in r, leaving out the Panes in omit, and appends the
// rectangles and separators to a.
func place(n layoutNode, r rect, omit map[Zone]bool, a *arrangement) {
	if n.pane != "" {
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
	sum := 0
	for _, wt := range weights {
		sum += wt
	}
	extent := r.h
	if n.dir == splitRow {
		extent = r.w
	}
	// Each child gets its share of the whole extent; the last one takes what
	// is left after the separators.
	at := 0
	for i, k := range kids {
		size := extent * weights[i] / sum
		if i == len(kids)-1 {
			size = extent - at
		}
		if n.dir == splitRow {
			place(k, rect{r.x + at, r.y, size, r.h}, omit, a)
			at += size
			if i < len(kids)-1 {
				a.seps = append(a.seps, rect{r.x + at, r.y, 1, r.h})
				at++
			}
		} else {
			place(k, rect{r.x, r.y + at, r.w, size}, omit, a)
			at += size
		}
	}
}

// omitted reports whether every Pane under n is left out.
func (n layoutNode) omitted(omit map[Zone]bool) bool {
	if n.pane != "" {
		return omit[n.pane]
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
