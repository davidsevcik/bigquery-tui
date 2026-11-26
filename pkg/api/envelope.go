package api

const SchemaVersion = 1

type Envelope struct {
	SchemaVersion int    `json:"schema_version"`
	Data          any    `json:"data"`
	Truncated     bool   `json:"truncated,omitempty"`
	NextCursor    string `json:"next_cursor,omitempty"`
}

type ErrorBody struct {
	Error ErrorInfo `json:"error"`
}

type ErrorInfo struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

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
