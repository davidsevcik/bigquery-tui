package favorites

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/davidsevcik/bigquery-tui/pkg/api"
	"github.com/davidsevcik/bigquery-tui/pkg/sqlx"
)

func Query(text, body string) api.Favorite {
	f := Parse(text)
	f.SQL = body
	return f
}

func Schema(f api.Favorite) []api.ParamSpec {
	decl := map[string]api.ParamSpec{}
	for _, p := range f.Params {
		decl[strings.ToLower(p.Name)] = p
	}
	var out []api.ParamSpec
	for _, n := range sqlx.Params(f.SQL) {
		if p, ok := decl[strings.ToLower(n)]; ok {
			p.Name = n
			out = append(out, p)
			continue
		}
		out = append(out, api.ParamSpec{Name: n})
	}
	return out
}

type Values struct {
	m        map[string]string
	prompted bool
}

func Given(values map[string]string) Values { return Values{m: values} }

func Prompted(values map[string]string) Values { return Values{m: values, prompted: true} }

type ParamError struct {
	Name string
	Err  error
}

func (e *ParamError) Error() string { return e.Err.Error() }
func (e *ParamError) Unwrap() error { return e.Err }

func Bind(q api.Favorite, v Values) ([]api.QueryParam, error) {
	seen := map[string]string{}
	for k := range v.m {
		fk := strings.ToLower(k)
		if other, dup := seen[fk]; dup {
			a, b := other, k
			if a > b {
				a, b = b, a
			}
			return nil, fmt.Errorf("the parameter %s is given twice (%s and %s; names ignore case)", fk, a, b)
		}
		seen[fk] = k
	}
	used := map[string]bool{}
	var out []api.QueryParam
	var missing []string
	for _, s := range Schema(q) {
		used[strings.ToLower(s.Name)] = true
		val, ok := lookupFold(v.m, s.Name)
		if v.prompted && strings.TrimSpace(val) == "" {
			ok = false
		}
		switch {
		case ok:
		case s.Default != nil:
			val = *s.Default
		case v.prompted:
		default:
			missing = append(missing, "@"+s.Name)
			continue
		}
		p, err := bindValue(s, val)
		if err != nil {
			return nil, &ParamError{Name: s.Name, Err: err}
		}
		out = append(out, p)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing value for %s (give name=value)", strings.Join(missing, ", "))
	}
	var extra []string
	for k := range v.m {
		if !used[strings.ToLower(k)] {
			extra = append(extra, k)
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		return nil, fmt.Errorf("the query does not use the parameter(s) %s", strings.Join(extra, ", "))
	}
	return out, nil
}

func lookupFold(m map[string]string, name string) (string, bool) {
	if v, ok := m[name]; ok {
		return v, true
	}
	for k, v := range m {
		if strings.EqualFold(k, name) {
			return v, true
		}
	}
	return "", false
}

func Remembered(params []api.QueryParam) map[string]string {
	out := map[string]string{}
	for _, p := range params {
		v := valueText(p)
		if p.Type != "" {
			v = p.Type + ":" + v
		}
		out[p.Name] = v
	}
	return out
}

func valueText(p api.QueryParam) string {
	if p.Null {
		return "null"
	}
	return p.Value
}

func bindValue(p api.ParamSpec, v string) (api.QueryParam, error) {
	if p.Type == "" {
		return sqlx.InferParam(p.Name, v), nil
	}
	if pre, rest, ok := strings.Cut(v, ":"); ok && strings.EqualFold(strings.TrimSpace(pre), p.Type) {
		v = strings.TrimPrefix(rest, " ")
	}
	q := api.QueryParam{Name: p.Name, Type: p.Type, Value: v}
	t := strings.TrimSpace(v)
	if strings.EqualFold(t, "null") {
		q.Null, q.Value = true, ""
		return q, nil
	}
	if p.Type != "STRING" {
		q.Value = t
		if t == "" {
			return q, fmt.Errorf("@%s needs a %s value", p.Name, p.Type)
		}
	}
	var err error
	switch p.Type {
	case "INT64":
		_, err = strconv.ParseInt(t, 10, 64)
	case "FLOAT64":
		_, err = strconv.ParseFloat(t, 64)
	case "BOOL":
		switch strings.ToLower(t) {
		case "true", "false":
			q.Value = strings.ToLower(t)
		default:
			err = errors.New("not true or false")
		}
	case "DATE":
		_, err = time.Parse("2006-01-02", t)
	case "DATETIME":
		err = parseAny(t, "2006-01-02 15:04:05.999999", "2006-01-02T15:04:05.999999", "2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02")
	case "TIMESTAMP":
		err = parseAny(t, time.RFC3339Nano, "2006-01-02 15:04:05.999999Z07:00", "2006-01-02 15:04:05.999999 MST",
			"2006-01-02 15:04:05.999999", "2006-01-02T15:04:05.999999", "2006-01-02 15:04", "2006-01-02")
	}
	if err != nil {
		return q, fmt.Errorf("@%s: %q is not a valid %s", p.Name, t, p.Type)
	}
	return q, nil
}

func parseAny(v string, layouts ...string) error {
	var err error
	for _, l := range layouts {
		if _, err = time.Parse(l, v); err == nil {
			return nil
		}
	}
	return err
}
