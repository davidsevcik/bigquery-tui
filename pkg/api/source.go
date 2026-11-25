package api

// Source names the frontend that started a Run. It is stored on the History
// entry (`source:` in History search) and, for every Source but the TUI, put
// on the job as the bqt_source label. The engine's agent methods also use it
// to pick a policy profile (see engine and policy).
type Source string

const (
	SourceTUI Source = "tui"
	SourceGUI Source = "gui"
	SourceCLI Source = "cli"
	SourceMCP Source = "mcp"
)

// Sources lists every Source, in the order History search offers them.
var Sources = []Source{SourceTUI, SourceGUI, SourceCLI, SourceMCP}
