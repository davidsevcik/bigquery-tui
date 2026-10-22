package main

import (
	"bytes"
	"encoding/json"
	"go/token"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestLayerGraphPutsImportersAbove(t *testing.T) {
	nodes := []string{"cmd", "cli", "tui", "engine", "store", "bq", "api", "lonely"}
	edges := []Edge{
		{"cmd", "cli"}, {"cli", "tui"}, {"cli", "engine"}, {"cli", "api"},
		{"tui", "engine"}, {"tui", "api"}, {"engine", "store"}, {"engine", "bq"},
		{"store", "api"}, {"bq", "api"},
	}
	ranks, layers := layerGraph(nodes, edges)

	for _, e := range edges {
		if ranks[e.From] <= ranks[e.To] {
			t.Errorf("edge %s -> %s: rank %d is not above %d", e.From, e.To, ranks[e.From], ranks[e.To])
		}
	}
	if ranks["api"] != 0 || ranks["lonely"] != 0 || ranks["cmd"] != 5 {
		t.Errorf("ranks = %v", ranks)
	}
	seen := map[string]bool{}
	for level, layer := range layers {
		for _, n := range layer {
			if seen[n] {
				t.Errorf("%s placed twice", n)
			}
			seen[n] = true
			if want := len(layers) - 1 - ranks[n]; level != want {
				t.Errorf("%s on row %d, want %d", n, level, want)
			}
		}
	}
	if len(seen) != len(nodes) {
		t.Errorf("placed %d nodes, want %d", len(seen), len(nodes))
	}

	_, again := layerGraph(nodes, edges)
	if !reflect.DeepEqual(layers, again) {
		t.Errorf("layout is not deterministic:\n%v\n%v", layers, again)
	}
}

func TestLayerGraphReducesCrossings(t *testing.T) {
	// Sorted by name the rows are x1 x2 over a b, so the edges cross until a
	// sweep swaps one row.
	nodes := []string{"x1", "x2", "a", "b"}
	edges := []Edge{{"x1", "b"}, {"x2", "a"}}
	_, layers := layerGraph(nodes, edges)
	if c := crossings(layers, edges); c != 0 {
		t.Errorf("crossings = %d, layers = %v", c, layers)
	}
}

func TestExtractAPI(t *testing.T) {
	fset := token.NewFileSet()
	files, warnings := parseFiles(fset, "testdata/sample", []string{"sample.go"})
	if len(warnings) > 0 {
		t.Fatal(warnings)
	}
	api, synopsis, err := extractAPI(fset, files, "example.com/sample")
	if err != nil {
		t.Fatal(err)
	}
	if synopsis != "Package sample is a fixture for the archmap API extraction test." {
		t.Errorf("synopsis = %q", synopsis)
	}

	types := map[string]TypeDoc{}
	for _, td := range api.Types {
		types[td.Name] = td
	}
	store, client, mode := types["Store"], types["Client"], types["Mode"]
	if store.Kind != "interface" || !strings.Contains(store.Sig, "Put(key, value string) error") {
		t.Errorf("Store = %+v", store.Symbol)
	}
	if client.Kind != "struct" || !strings.Contains(client.Sig, "Name string") {
		t.Errorf("Client = %+v", client.Symbol)
	}
	if strings.Contains(client.Sig, "store Store") || !strings.Contains(client.Sig, "unexported fields") {
		t.Errorf("Client signature should hide the unexported field:\n%s", client.Sig)
	}
	if client.Doc != "Client talks to a Store." {
		t.Errorf("Client doc = %q", client.Doc)
	}

	var memberNames []string
	for _, m := range client.Members {
		memberNames = append(memberNames, m.Kind+" "+m.Name)
	}
	if want := []string{"func NewClient", "method Fetch"}; !reflect.DeepEqual(memberNames, want) {
		t.Errorf("Client members = %v, want %v", memberNames, want)
	}
	if sig := client.Members[1].Sig; sig != "func (c *Client) Fetch(key string) (string, error)" {
		t.Errorf("Fetch signature = %q", sig)
	}
	if len(mode.Members) != 1 || mode.Members[0].Name != "ModeFast, ModeSafe" {
		t.Errorf("Mode members = %+v", mode.Members)
	}

	if len(api.Funcs) != 1 || api.Funcs[0].Name != "Version" {
		t.Errorf("funcs = %+v", api.Funcs)
	}
	if len(api.Consts) != 1 || api.Consts[0].Name != "MaxItems" {
		t.Errorf("consts = %+v", api.Consts)
	}
	all, _ := json.Marshal(api)
	for _, hidden := range []string{"helper", "unexported"} {
		if bytes.Contains(all, []byte(`"`+hidden+`"`)) {
			t.Errorf("unexported %s leaked into the API", hidden)
		}
	}
	// 3 types + NewClient + Fetch + Mode's const group + Version + MaxItems.
	if n := api.count(); n != 8 {
		t.Errorf("count = %d, want 8", n)
	}
}

func TestLoadThisModule(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go tool not on PATH")
	}
	m, warnings, err := Load(".")
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) > 0 {
		t.Errorf("warnings: %v", warnings)
	}
	byShort := map[string]*Package{}
	for _, p := range m.Packages {
		byShort[p.Short] = p
		if strings.HasPrefix(p.Short, "internal/tools/") {
			t.Errorf("the generator itself is on the map: %s", p.Short)
		}
	}
	for _, want := range []string{"cmd/bqt", "internal/api", "internal/engine", "internal/tui"} {
		if byShort[want] == nil {
			t.Fatalf("package %s missing", want)
		}
	}
	if imps := byShort["internal/api"].Imports; len(imps) != 0 {
		t.Errorf("internal/api imports %v, want nothing", imps)
	}
	if g := byShort["internal/tui"].Group; g != "frontend" {
		t.Errorf("tui group = %s", g)
	}
	if g := byShort["internal/engine"].Group; g != "core" {
		t.Errorf("engine group = %s", g)
	}
	if p := byShort["internal/archtest"]; p == nil || p.Group != "test" {
		t.Errorf("archtest should be a test-only package: %+v", p)
	}
	if !hasEdge(m.Edges, "internal/engine", "internal/runs") {
		t.Error("edge internal/engine -> internal/runs missing")
	}
	if byShort["internal/engine"].Exported == 0 {
		t.Error("engine has no exported API")
	}
	if byShort["cmd/bqt"].Y >= byShort["internal/api"].Y {
		t.Error("cmd/bqt should be drawn above internal/api")
	}
	if m.Layout.Hub != "internal/api" {
		t.Errorf("hub = %s, want internal/api", m.Layout.Hub)
	}
	ext := map[string]bool{}
	for _, e := range m.External {
		ext[e.Module] = true
	}
	if !ext["cloud.google.com/go/bigquery"] || !ext["charm.land/bubbletea/v2"] {
		t.Errorf("third-party modules = %v", ext)
	}

	var buf bytes.Buffer
	if err := render(&buf, m); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	if strings.Contains(html, string(dataMarker)) || !strings.Contains(html, `"module":"github.com/davidsevcik/bigquery-tui"`) {
		t.Error("model JSON was not embedded in the page")
	}
	if strings.Contains(html, ":null") {
		t.Error("the model has a null list; the page script expects arrays")
	}
}

func hasEdge(edges []Edge, from, to string) bool {
	for _, e := range edges {
		if e.From == from && e.To == to {
			return true
		}
	}
	return false
}
