package api

// SchemaVersion is the version of the JSON contract. Changes within a
// version are additive only.
const SchemaVersion = 1

// Envelope wraps every successful non-TUI command result.
type Envelope struct {
	SchemaVersion int    `json:"schema_version"`
	Data          any    `json:"data"`
	Truncated     bool   `json:"truncated,omitempty"`
	NextCursor    string `json:"next_cursor,omitempty"`
}

// ErrorBody is the JSON written to stderr when a command fails.
type ErrorBody struct {
	Error ErrorInfo `json:"error"`
}

// ErrorInfo describes a failure.
type ErrorInfo struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

// Exit codes (documented contract).
const (
	ExitOK        = 0
	ExitFailure   = 1
	ExitUsage     = 2
	ExitConfig    = 3
	ExitAuth      = 4
	ExitOverLimit = 5
	ExitBigQuery  = 6
	ExitCancelled = 7
)
