package sqlx

import (
	"testing"

	"github.com/davidsevcik/bigquery-tui/pkg/api"
)

func TestAccept(t *testing.T) {
	for _, c := range []struct {
		name string
		text string
		cand api.Candidate
		want Acceptance
	}{
		{
			"a project is followed by a dot and completion goes on", "select * from my",
			api.Candidate{Insert: "my-proj", Kind: api.CandProject, Start: 14, End: 16},
			Acceptance{Insert: "my-proj.", Again: true},
		},
		{
			"a dataset steps over the dot already there", "select * from p.sa.orders",
			api.Candidate{Insert: "sales", Kind: api.CandDataset, Start: 16, End: 18},
			Acceptance{Insert: "sales", StepOver: true, Again: true},
		},
		{
			"a table closes the backtick path the user opened", "select * from `p.d.or",
			api.Candidate{Insert: "orders", Kind: api.CandTable, Start: 19, End: 21},
			Acceptance{Insert: "orders`"},
		},
		{
			"a closing backtick already there is kept", "select * from `p.d.or`",
			api.Candidate{Insert: "orders", Kind: api.CandTable, Start: 19, End: 21},
			Acceptance{Insert: "orders"},
		},
		{
			"a table quoted on its own is left alone", "select * from `p.d.`",
			api.Candidate{Insert: "`orders-1`", Kind: api.CandTable, Start: 19, End: 19},
			Acceptance{Insert: "`orders-1`"},
		},
		{
			"an unquoted table", "select * from p.d.or",
			api.Candidate{Insert: "orders", Kind: api.CandTable, Start: 18, End: 20},
			Acceptance{Insert: "orders"},
		},
		{
			"a column is inserted as it is", "select na",
			api.Candidate{Insert: "name", Kind: api.CandColumn, Start: 7, End: 9},
			Acceptance{Insert: "name"},
		},
	} {
		if got := Accept(c.text, c.cand); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}
