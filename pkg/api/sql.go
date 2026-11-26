package api

type TokenKind string

const (
	TokKeyword  TokenKind = "keyword"
	TokString   TokenKind = "string"
	TokNumber   TokenKind = "number"
	TokComment  TokenKind = "comment"
	TokQuoted   TokenKind = "quoted"
	TokFunction TokenKind = "function"
	TokParam    TokenKind = "param"
	TokIdent    TokenKind = "ident"
	TokPunct    TokenKind = "punct"
)

type Token struct {
	Start int       `json:"start"`
	End   int       `json:"end"`
	Kind  TokenKind `json:"kind"`
}

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

type Candidate struct {
	Label  string        `json:"label"`
	Insert string        `json:"insert"`
	Kind   CandidateKind `json:"kind"`
	Detail string        `json:"detail,omitempty"`
	Start  int           `json:"start"`
	End    int           `json:"end"`
}

type QueryParam struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
	Null  bool   `json:"null,omitempty"`
}
