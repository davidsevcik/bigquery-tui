package api

// Source names the frontend that started a Run. It is stored on the History
// entry (`source:` in History search) and, for every Source but the TUI, put
// on the job as the bqt_source label. Only the engine's agent methods use it
// to decide what a Run may do: they pick the policy profile from it (MCP, or
// the CLI's for any other Source). Runs started any other way, as the TUI and
// a GUI start them, get no agent limits whatever their Source.
type Source string

const (
	SourceTUI Source = "tui"
	SourceGUI Source = "gui"
	SourceCLI Source = "cli"
	SourceMCP Source = "mcp"
)

// Sources lists every Source, in the order History search offers them.
var Sources = []Source{SourceTUI, SourceGUI, SourceCLI, SourceMCP}
