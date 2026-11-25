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

// QueryParam is one named query parameter with its value as text.
type QueryParam struct {
	Name  string `json:"name"`
	Type  string `json:"type"` // STRING, INT64, FLOAT64, BOOL, DATE, DATETIME, TIMESTAMP
	Value string `json:"value"`
	Null  bool   `json:"null,omitempty"`
}
