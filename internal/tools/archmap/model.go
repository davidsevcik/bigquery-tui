package main

// Model is everything the page needs. It is written into the page as JSON.
type Model struct {
	Module    string      `json:"module"`
	Generated string      `json:"generated,omitempty"`
	Packages  []*Package  `json:"packages"`
	Edges     []Edge      `json:"edges"`
	External  []*External `json:"external"`
	Layout    Canvas      `json:"layout"`

	root string // module root directory on disk
}

// Package is one package of the module.
type Package struct {
	Path       string   `json:"path"`  // full import path
	Short      string   `json:"short"` // path inside the module, e.g. internal/bq
	Label      string   `json:"label"` // Short without the leading internal/
	Name       string   `json:"name"`
	Group      string   `json:"group"` // frontend, core or test
	Doc        string   `json:"doc"`
	Files      int      `json:"files"`
	Exported   int      `json:"exported"`
	Imports    []string `json:"imports"`    // Short paths of module packages
	ImportedBy []string `json:"importedBy"` // Short paths of module packages
	ThirdParty []string `json:"thirdParty"` // module paths
	API        API      `json:"api"`
	X          float64  `json:"x"`
	Y          float64  `json:"y"`
	Rank       int      `json:"rank"` // 0 = imports no module package

	dir     string
	goFiles []string
}

// Edge is a direct import from one module package to another (Short paths).
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// External is a third-party module imported directly by module packages.
type External struct {
	Module    string   `json:"module"`
	Importers []string `json:"importers"`
	X         float64  `json:"x"`
	Y         float64  `json:"y"`
}

// API is a package's exported surface.
type API struct {
	Types  []TypeDoc `json:"types"`
	Funcs  []Symbol  `json:"funcs"`
	Consts []Symbol  `json:"consts"`
	Vars   []Symbol  `json:"vars"`
}

// Symbol is one exported declaration.
type Symbol struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // func, method, const, var, struct, interface, alias, type, func type
	Sig  string `json:"sig"`
	Doc  string `json:"doc,omitempty"`
}

// TypeDoc is an exported type with its constructors, methods and typed values.
type TypeDoc struct {
	Symbol
	Members []Symbol `json:"members"`
}

// Canvas holds the drawing sizes the page uses.
type Canvas struct {
	NodeW        float64 `json:"nodeW"`
	NodeH        float64 `json:"nodeH"`
	ExtW         float64 `json:"extW"`
	ExtH         float64 `json:"extH"`
	Width        float64 `json:"width"`
	Height       float64 `json:"height"`       // without the third-party row
	HeightAll    float64 `json:"heightAll"`    // with the third-party row
	Levels       int     `json:"levels"`       // number of package rows
	Hub          string  `json:"hub"`          // the most imported package
	HubImporters int     `json:"hubImporters"` // how many packages import it
}

func (a API) count() int {
	n := len(a.Funcs) + len(a.Consts) + len(a.Vars)
	for _, t := range a.Types {
		n += 1 + len(t.Members)
	}
	return n
}
