package api

// TokenKind classifies a span of SQL text.
type TokenKind string

const (
	TokKeyword  TokenKind = "keyword"
	TokString   TokenKind = "string"
	TokNumber   TokenKind = "number"
	TokComment  TokenKind = "comment"
	TokQuoted   TokenKind = "quoted" // backtick identifier
	TokFunction TokenKind = "function"
	TokParam    TokenKind = "param"
	TokIdent    TokenKind = "ident"
	TokPunct    TokenKind = "punct"
)

// Token is one lexical span. Start and End are UTF-8 byte offsets; whitespace
// produces no token.
type Token struct {
	Start int       `json:"start"`
	End   int       `json:"end"`
	Kind  TokenKind `json:"kind"`
}

// CandidateKind says what a completion candidate is.
type CandidateKind string

const (
	CandProject  CandidateKind = "project"
	CandDataset  CandidateKind = "dataset"
	CandTable    CandidateKind = "table"
	CandColumn   CandidateKind = "column"
	CandCTE      CandidateKind = "cte"
	CandAlias    CandidateKind = "alias"
	CandFunction CandidateKind = "function"
	CandKeyword  CandidateKind = "keyword"
)

// Candidate is one completion suggestion. Accepting it replaces the bytes
// [Start, End) of the text with Insert.
type Candidate struct {
	Label  string        `json:"label"`
	Insert string        `json:"insert"`
	Kind   CandidateKind `json:"kind"`
	// Detail is a type, a signature or the object's kind.
	Detail string `json:"detail,omitempty"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
}

// CompleteOptions tunes a completion request.
type CompleteOptions struct {
	// Forced lifts the "two typed characters" rule (ctrl+space).
	Forced bool `json:"forced,omitempty"`
	// Projects limits the projects offered, with their datasets and tables,
	// to these among the Selected projects; empty means every Selected
	// project. It does not limit what the text names in full: the columns
	// of a project.dataset.table are offered wherever the table is.
	Projects []string `json:"projects,omitempty"`
	// Execution is the Execution project the text will run in, where an
	// unqualified dataset.table resolves; its datasets and tables are
	// offered whether or not Projects names it. Empty means the shared
	// Execution project.
	Execution string `json:"execution,omitempty"`
}

// QueryParam is one named query parameter with its value as text.
type QueryParam struct {
	Name  string `json:"name"`
	Type  string `json:"type"` // STRING, INT64, FLOAT64, BOOL, DATE, DATETIME, TIMESTAMP
	Value string `json:"value"`
	Null  bool   `json:"null,omitempty"`
}
