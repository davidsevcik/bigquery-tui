package main

type Model struct {
	Module    string      `json:"module"`
	Generated string      `json:"generated,omitempty"`
	Packages  []*Package  `json:"packages"`
	Edges     []Edge      `json:"edges"`
	External  []*External `json:"external"`
	Layout    Canvas      `json:"layout"`

	root string
}

type Package struct {
	Path       string   `json:"path"`
	Short      string   `json:"short"`
	Label      string   `json:"label"`
	Name       string   `json:"name"`
	Group      string   `json:"group"`
	Doc        string   `json:"doc"`
	Files      int      `json:"files"`
	Exported   int      `json:"exported"`
	Imports    []string `json:"imports"`
	ImportedBy []string `json:"importedBy"`
	ThirdParty []string `json:"thirdParty"`
	API        API      `json:"api"`
	X          float64  `json:"x"`
	Y          float64  `json:"y"`
	Rank       int      `json:"rank"`

	dir     string
	goFiles []string
}

type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type External struct {
	Module    string   `json:"module"`
	Importers []string `json:"importers"`
	X         float64  `json:"x"`
	Y         float64  `json:"y"`
}

type API struct {
	Types  []TypeDoc `json:"types"`
	Funcs  []Symbol  `json:"funcs"`
	Consts []Symbol  `json:"consts"`
	Vars   []Symbol  `json:"vars"`
}

type Symbol struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Sig  string `json:"sig"`
	Doc  string `json:"doc,omitempty"`
}

type TypeDoc struct {
	Symbol
	Members []Symbol `json:"members"`
}

type Canvas struct {
	NodeW        float64 `json:"nodeW"`
	NodeH        float64 `json:"nodeH"`
	ExtW         float64 `json:"extW"`
	ExtH         float64 `json:"extH"`
	Width        float64 `json:"width"`
	Height       float64 `json:"height"`
	HeightAll    float64 `json:"heightAll"`
	Levels       int     `json:"levels"`
	Hub          string  `json:"hub"`
	HubImporters int     `json:"hubImporters"`
}

func (a API) count() int {
	n := len(a.Funcs) + len(a.Consts) + len(a.Vars)
	for _, t := range a.Types {
		n += 1 + len(t.Members)
	}
	return n
}
