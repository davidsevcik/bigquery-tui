package favorites

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/sqlx"
)

// Schema is the parameter schema of a query: the parameters the body uses,
// in order of first use, typed by the header declarations. A parameter used
// but not declared has no type (its type is guessed from the value); a
// declared one the body does not use is left out (BigQuery would refuse it).
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

// Bind turns a typed value into a query parameter. An empty value takes the
// default; "null" is NULL for every type. Without a declared type the type
// is guessed from the value (a TYPE: prefix forces it), as for any query.
func Bind(p api.ParamSpec, value string) (api.QueryParam, error) {
	v := value
	if strings.TrimSpace(v) == "" && p.Default != nil {
		v = *p.Default
	}
	if p.Type == "" {
		return sqlx.InferParam(p.Name, v), nil
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
			err = fmt.Errorf("not true or false")
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
