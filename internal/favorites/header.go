package favorites

import (
	"strings"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/sqlx"
)

// SaveRequest saves a text (an editor buffer) as a Favorite with a fresh
// header: the description and tags given, the -- param: lines it already
// has, and its body as it is.
type SaveRequest struct {
	Text        string
	Name        string // the Favorite's name: a path under the directory
	Description string
	Tags        []string
	// Own is the ID the text is already saved as ("" for none): saving
	// under that name again replaces it without asking.
	Own string
	// Overwrite replaces another Favorite of that name.
	Overwrite bool
}

// Saved is a Favorite just written, and the edit that gives the text the
// same header.
type Saved struct {
	Favorite api.Favorite
	Header   HeaderEdit
}

// HeaderEdit replaces the header of a text: its first Old bytes become New.
// The body after them is unchanged.
type HeaderEdit struct {
	Old int
	New string
}

// Save writes the text with its new header. Another existing Favorite of
// that name is an ErrExists unless Overwrite is set.
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
		newHead-- // Render ended the body with a newline the text does not have
	}
	return Saved{Favorite: saved, Header: HeaderEdit{Old: len(r.Text) - len(f.SQL), New: out[:newHead]}}, nil
}

// FromHistory is the file text of a History entry promoted to a Favorite.
// It starts from the header the SQL may already carry (a Run from a
// Favorite's buffer), so the new one replaces it instead of stacking on it;
// a description or tags given replace the old ones; and every other
// parameter the SQL uses is declared with the type and value it ran with as
// its default (NULL as null).
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
