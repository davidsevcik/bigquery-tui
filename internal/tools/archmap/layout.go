package main

import (
	"math"
	"sort"
)

// Drawing sizes, in SVG units.
const (
	nodeW   = 156.0
	nodeH   = 42.0
	colGap  = 22.0
	rowH    = 92.0
	margin  = 24.0
	extW    = 176.0
	extH    = 30.0
	extGap  = 12.0
	extDrop = 28.0 // extra space above the third-party row
	sweeps  = 24
)

// arrange gives every graph package and third-party module a position.
// Packages sit in rows by rank (longest import chain below them), so
// importers are always above what they import. Test-only packages are not
// drawn.
func arrange(m *Model) {
	var nodes []string
	byShort := map[string]*Package{}
	for _, p := range m.Packages {
		if p.Group == "test" {
			continue
		}
		nodes = append(nodes, p.Short)
		byShort[p.Short] = p
	}
	ranks, layers := layerGraph(nodes, m.Edges)
	for s, r := range ranks {
		byShort[s].Rank = r
	}

	// Positions relative to the centre line, in columns.
	rel := map[string]float64{}
	widest := 0
	for _, layer := range layers {
		widest = max(widest, len(layer))
		for i, n := range layer {
			rel[n] = float64(i) - float64(len(layer)-1)/2
		}
	}

	// Third-party modules: one row under the packages, ordered by the mean
	// position of their importers.
	ext := append([]*External(nil), m.External...)
	key := map[string]float64{}
	for _, e := range ext {
		key[e.Module] = mean(e.Importers, rel, 0)
	}
	sort.SliceStable(ext, func(i, j int) bool { return key[ext[i].Module] < key[ext[j].Module] })

	colW := nodeW + colGap
	extColW := extW + extGap
	inner := float64(widest)*colW - colGap
	perRow := max(1, int((inner+extGap)/extColW))
	width := math.Max(inner, float64(min(perRow, len(ext)))*extColW-extGap) + 2*margin
	center := width / 2

	for _, n := range nodes {
		level := len(layers) - 1 - ranks[n]
		byShort[n].X = center + rel[n]*colW
		byShort[n].Y = margin + nodeH/2 + float64(level)*rowH
	}
	height := 2*margin + nodeH + float64(max(len(layers)-1, 0))*rowH
	// Wrap the third-party modules into rows no wider than the packages.
	extTop := height - margin + extDrop
	extRows := (len(ext) + perRow - 1) / perRow
	for i, e := range ext {
		row, col := i/perRow, i%perRow
		inRow := min(perRow, len(ext)-row*perRow)
		e.X = center + (float64(col)-float64(inRow-1)/2)*extColW
		e.Y = extTop + extH/2 + float64(row)*(extH+extGap)
	}
	heightAll := height
	if extRows > 0 {
		heightAll = extTop + float64(extRows)*(extH+extGap) - extGap + margin
	}

	hub, hubN := "", 0
	for _, p := range m.Packages {
		if n := len(p.ImportedBy); n > hubN {
			hub, hubN = p.Short, n
		}
	}
	m.Layout = Canvas{
		NodeW: nodeW, NodeH: nodeH, ExtW: extW, ExtH: extH,
		Width: width, Height: height, HeightAll: heightAll,
		Levels: len(layers), Hub: hub, HubImporters: hubN,
	}
}

// layerGraph ranks the nodes (0 = imports nothing in the graph) and orders
// each row to reduce edge crossings. layers[0] is the top row (highest rank).
func layerGraph(nodes []string, edges []Edge) (map[string]int, [][]string) {
	in := map[string]bool{}
	for _, n := range nodes {
		in[n] = true
	}
	out, parents := map[string][]string{}, map[string][]string{}
	var graphEdges []Edge
	for _, e := range edges {
		if in[e.From] && in[e.To] {
			out[e.From] = append(out[e.From], e.To)
			parents[e.To] = append(parents[e.To], e.From)
			graphEdges = append(graphEdges, e)
		}
	}

	sorted := append([]string(nil), nodes...)
	sort.Strings(sorted)
	rank := map[string]int{}
	onStack := map[string]bool{}
	var visit func(string) int
	visit = func(n string) int {
		if r, ok := rank[n]; ok {
			return r
		}
		if onStack[n] { // Go forbids import cycles; stay safe anyway.
			return 0
		}
		onStack[n] = true
		r := 0
		for _, m := range out[n] {
			r = max(r, visit(m)+1)
		}
		delete(onStack, n)
		rank[n] = r
		return r
	}
	top := 0
	for _, n := range sorted {
		top = max(top, visit(n))
	}
	if len(sorted) == 0 {
		return rank, nil
	}
	layers := make([][]string, top+1)
	for _, n := range sorted {
		level := top - rank[n]
		layers[level] = append(layers[level], n)
	}

	best, bestC := clone(layers), crossings(layers, graphEdges)
	for it := 0; it < sweeps && bestC > 0; it++ {
		x := positions(layers)
		down := it%2 == 0
		for k := range layers {
			level := k
			nbrs := parents
			if !down {
				level = len(layers) - 1 - k
				nbrs = out
			}
			layer := layers[level]
			key := map[string]float64{}
			for _, n := range layer {
				key[n] = mean(nbrs[n], x, x[n])
			}
			sort.SliceStable(layer, func(i, j int) bool { return key[layer[i]] < key[layer[j]] })
			for i, n := range layer {
				x[n] = float64(i) - float64(len(layer)-1)/2
			}
		}
		if c := crossings(layers, graphEdges); c < bestC {
			best, bestC = clone(layers), c
		}
	}
	return rank, best
}

func positions(layers [][]string) map[string]float64 {
	x := map[string]float64{}
	for _, layer := range layers {
		for i, n := range layer {
			x[n] = float64(i) - float64(len(layer)-1)/2
		}
	}
	return x
}

// crossings counts pairs of edges whose straight lines cross.
func crossings(layers [][]string, edges []Edge) int {
	x := positions(layers)
	y := map[string]float64{}
	for level, layer := range layers {
		for _, n := range layer {
			y[n] = float64(level) * 2 // rows are about twice as far apart as columns
		}
	}
	c := 0
	for i := range edges {
		a, b := edges[i].From, edges[i].To
		for j := i + 1; j < len(edges); j++ {
			p, q := edges[j].From, edges[j].To
			if a == p || a == q || b == p || b == q {
				continue
			}
			if segmentsCross(x[a], y[a], x[b], y[b], x[p], y[p], x[q], y[q]) {
				c++
			}
		}
	}
	return c
}

func segmentsCross(ax, ay, bx, by, cx, cy, dx, dy float64) bool {
	d1 := orient(cx, cy, dx, dy, ax, ay)
	d2 := orient(cx, cy, dx, dy, bx, by)
	d3 := orient(ax, ay, bx, by, cx, cy)
	d4 := orient(ax, ay, bx, by, dx, dy)
	return d1*d2 < 0 && d3*d4 < 0
}

func orient(ax, ay, bx, by, cx, cy float64) float64 {
	return (bx-ax)*(cy-ay) - (by-ay)*(cx-ax)
}

func mean(ns []string, x map[string]float64, fallback float64) float64 {
	if len(ns) == 0 {
		return fallback
	}
	sum := 0.0
	for _, n := range ns {
		sum += x[n]
	}
	return sum / float64(len(ns))
}

func clone(layers [][]string) [][]string {
	out := make([][]string, len(layers))
	for i, l := range layers {
		out[i] = append([]string(nil), l...)
	}
	return out
}
