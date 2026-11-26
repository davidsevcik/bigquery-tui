package api

type Source string

const (
	SourceTUI Source = "tui"
	SourceGUI Source = "gui"
	SourceCLI Source = "cli"
	SourceMCP Source = "mcp"
)

var Sources = []Source{SourceTUI, SourceGUI, SourceCLI, SourceMCP}
