package sqlx

import (
	"strings"

	"github.com/davidsevcik/bigquery-tui/pkg/api"
)

// Acceptance is how accepting a completion candidate changes the text.
type Acceptance struct {
	// Insert replaces text[c.Start:c.End].
	Insert string
	// StepOver moves the cursor one byte further, past the dot that already
	// follows the insert.
	StepOver bool
	// Again means completion continues at once, with the next path segment.
	Again bool
}

// Accept is what accepting candidate c (from Complete on text) does. A
// project or dataset is followed by a dot, or steps over the dot already
// there, and completion continues with the next segment. A table closes a
// backtick path the user opened.
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
