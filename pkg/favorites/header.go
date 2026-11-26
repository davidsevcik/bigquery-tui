package favorites

import (
	"strings"

	"github.com/davidsevcik/bigquery-tui/pkg/api"
	"github.com/davidsevcik/bigquery-tui/pkg/sqlx"
)

type SaveRequest struct {
	Text        string
	Name        string
	Description string
	Tags        []string
	Own         string
	Overwrite   bool
}

type Saved struct {
	Favorite api.Favorite
	Header   HeaderEdit
}

type HeaderEdit struct {
	Old int
	New string
}

func (d Dir) Save(r SaveRequest) (Saved, error) {
	f := Parse(r.Text)
	f.Description, f.Tags = strings.TrimSpace(r.Description), r.Tags
	out := Render(f)
	overwrite := r.Overwrite
	if r.Own != "" {
		own, err1 := CleanID(r.Own)
		name, err2 := CleanID(r.Name)
		overwrite = overwrite || (err1 == nil && err2 == nil && own == name)
	}
	id, err := d.Write(r.Name, out, overwrite)
	if err != nil {
		return Saved{}, err
	}
	saved, err := d.Read(id)
	if err != nil {
		return Saved{}, err
	}
	newHead := len(out) - len(f.SQL)
	if f.SQL != "" && !strings.HasSuffix(f.SQL, "\n") {
		newHead--
	}
	return Saved{Favorite: saved, Header: HeaderEdit{Old: len(r.Text) - len(f.SQL), New: out[:newHead]}}, nil
}

func FromHistory(sql string, params []api.QueryParam, description string, tags []string) string {
	f := Parse(sql)
	f.Text, f.Problems = "", nil
	if strings.TrimSpace(description) != "" || len(f.Description) == 0 {
		f.Description = description
	}
	if len(tags) > 0 {
		f.Tags = tags
	}
	declared := map[string]bool{}
	for _, p := range f.Params {
		declared[strings.ToLower(p.Name)] = true
	}
	used := map[string]bool{}
	for _, n := range sqlx.Params(f.SQL) {
		used[strings.ToLower(n)] = true
	}
	for _, p := range params {
		k := strings.ToLower(p.Name)
		if !used[k] || declared[k] || !validType(p.Type) {
			continue
		}
		v := valueText(p)
		f.Params = append(f.Params, api.ParamSpec{Name: p.Name, Type: p.Type, Default: &v})
	}
	return Render(f)
}
