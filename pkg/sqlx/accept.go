package sqlx

import (
	"strings"

	"github.com/davidsevcik/bigquery-tui/pkg/api"
)

type Acceptance struct {
	Insert   string
	StepOver bool
	Again    bool
}

func Accept(text string, c api.Candidate) Acceptance {
	a := Acceptance{Insert: c.Insert}
	after := ""
	if c.End < len(text) {
		after = text[c.End:]
	}
	switch c.Kind {
	case api.CandProject, api.CandDataset:
		if strings.HasPrefix(after, ".") {
			a.StepOver = true
		} else {
			a.Insert += "."
		}
		a.Again = true
	case api.CandTable:
		if strings.Count(text[:c.Start], "`")%2 == 1 && !strings.HasPrefix(after, "`") && !strings.HasPrefix(a.Insert, "`") {
			a.Insert += "`"
		}
	}
	return a
}
