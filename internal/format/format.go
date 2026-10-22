// Package format turns BigQuery wire values into display cells. It is core
// logic shared by every frontend: frontends only lay the cells out. Output
// never contains control characters or ANSI sequences.
package format

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq"
)

// Options controls cell formatting.
type Options struct {
	// MaxChars truncates a cell's text (in characters). 0 means no limit.
	MaxChars int
	// ArraySummary shows a REPEATED value as "[3] first, second, third"
	// instead of JSON (the results grid).
	ArraySummary bool
	// KeepNewlines leaves line breaks in the text (the record view wraps
	// them); other control characters are still replaced.
	KeepNewlines bool
}

// Row formats one wire row against its schema.
func Row(schema []bq.Field, raw []any, o Options) []api.Cell {
	out := make([]api.Cell, len(schema))
	for i, f := range schema {
		var v any
		if i < len(raw) {
			v = raw[i]
		}
		out[i] = Cell(f, v, o)
	}
	return out
}

// Cell formats one wire value. Scalars are shown as text; RECORD, REPEATED
// and JSON values as compact one-line JSON.
func Cell(f bq.Field, v any, o Options) api.Cell {
	if v == nil {
		return api.Cell{Null: true}
	}
	text, _ := Text(f, v)
	if o.ArraySummary && f.Mode == "REPEATED" {
		text = arraySummary(f, v)
	}
	return clip(sanitize(text, o.KeepNewlines), o.MaxChars)
}

// Text is the full, unsanitized text of a wire value (export): scalars as
// they are, RECORD, REPEATED and JSON values as compact JSON. null reports a
// NULL value.
func Text(f bq.Field, v any) (text string, null bool) {
	if v == nil {
		return "", true
	}
	if f.Mode == "REPEATED" || isRecord(f) || f.Type == "JSON" {
		return JSONValue(f, v), false
	}
	return scalarText(f.Type, v), false
}

// JSONValue renders a wire value as compact JSON, keeping field order and
// exact numeric text.
func JSONValue(f bq.Field, v any) string {
	var b strings.Builder
	writeJSON(&b, f, v, f.Mode == "REPEATED")
	return b.String()
}

func arraySummary(f bq.Field, v any) string {
	items, _ := v.([]any)
	var b strings.Builder
	b.WriteString("[" + strconv.Itoa(len(items)) + "]")
	elem := f
	elem.Mode = ""
	for i, it := range items {
		if m, ok := it.(map[string]any); ok {
			it = m["v"]
		}
		if i == 0 {
			b.WriteByte(' ')
		} else {
			b.WriteString(", ")
		}
		if it == nil {
			b.WriteString("NULL")
			continue
		}
		t, _ := Text(elem, it)
		b.WriteString(t)
		if b.Len() > 4096 {
			break // the cell is clipped anyway
		}
	}
	return b.String()
}

func isRecord(f bq.Field) bool { return f.Type == "RECORD" || f.Type == "STRUCT" }

func scalarText(typ string, v any) string {
	s, ok := v.(string)
	if !ok {
		b, _ := json.Marshal(v)
		return string(b)
	}
	if typ == "TIMESTAMP" {
		if us, err := strconv.ParseInt(s, 10, 64); err == nil {
			return time.UnixMicro(us).UTC().Format("2006-01-02 15:04:05.999999 UTC")
		}
	}
	return s
}

// writeJSON renders a wire value as compact JSON, keeping field order and
// exact numeric text (INT64 and NUMERIC are never routed through floats).
func writeJSON(b *strings.Builder, f bq.Field, v any, repeated bool) {
	if v == nil {
		b.WriteString("null")
		return
	}
	if repeated {
		items, _ := v.([]any)
		b.WriteByte('[')
		for i, it := range items {
			if i > 0 {
				b.WriteByte(',')
			}
			if m, ok := it.(map[string]any); ok {
				it = m["v"]
			}
			writeJSON(b, f, it, false)
		}
		b.WriteByte(']')
		return
	}
	if isRecord(f) {
		m, _ := v.(map[string]any)
		cells, _ := m["f"].([]any)
		b.WriteByte('{')
		for i, sub := range f.Fields {
			if i > 0 {
				b.WriteByte(',')
			}
			k, _ := json.Marshal(sub.Name)
			b.Write(k)
			b.WriteByte(':')
			var cv any
			if i < len(cells) {
				if cm, ok := cells[i].(map[string]any); ok {
					cv = cm["v"]
				}
			}
			writeJSON(b, sub, cv, sub.Mode == "REPEATED")
		}
		b.WriteByte('}')
		return
	}
	s, ok := v.(string)
	if !ok {
		j, _ := json.Marshal(v)
		b.Write(j)
		return
	}
	switch f.Type {
	case "INTEGER", "INT64", "FLOAT", "FLOAT64", "NUMERIC", "BIGNUMERIC":
		if looksNumeric(s) {
			b.WriteString(s)
			return
		}
	case "BOOLEAN", "BOOL":
		if s == "true" || s == "false" {
			b.WriteString(s)
			return
		}
	case "JSON":
		if json.Valid([]byte(s)) {
			var c strings.Builder
			if err := compact(&c, s); err == nil {
				b.WriteString(c.String())
				return
			}
		}
	case "TIMESTAMP":
		s = scalarText("TIMESTAMP", s)
	}
	j, _ := json.Marshal(s)
	b.Write(j)
}

func compact(dst *strings.Builder, s string) error {
	var raw json.RawMessage
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return err
	}
	out, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	dst.Write(out)
	return nil
}

func looksNumeric(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9') && c != '-' && c != '+' && c != '.' && c != 'e' && c != 'E' {
			return false
		}
	}
	return true
}

// sanitize replaces control characters so a cell is always one safe line.
func sanitize(s string, keepNewlines bool) string {
	clean := true
	for i := 0; i < len(s); i++ {
		if (s[i] < 0x20 && !(keepNewlines && s[i] == '\n')) || s[i] == 0x7f {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' && keepNewlines:
			b.WriteByte('\n')
		case r == '\n':
			b.WriteString("⏎")
		case r == '\t':
			b.WriteByte(' ')
		case r < 0x20 || r == 0x7f:
			b.WriteRune('�')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func clip(s string, maxChars int) api.Cell {
	if maxChars <= 0 || utf8.RuneCountInString(s) <= maxChars {
		return api.Cell{Text: s}
	}
	n, i := 0, 0
	for i = range s {
		if n == maxChars {
			break
		}
		n++
	}
	return api.Cell{Text: s[:i], Truncated: true}
}

// Columns derives the preview headers from a schema.
func Columns(schema []bq.Field) []api.PreviewColumn {
	out := make([]api.PreviewColumn, len(schema))
	for i, f := range schema {
		out[i] = api.PreviewColumn{Name: f.Name, Type: f.Type, Mode: f.Mode}
	}
	return out
}

// Bytes renders a byte count for humans (binary units).
func Bytes(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return strconv.FormatFloat(float64(n)/float64(div), 'f', 1, 64) + " " + []string{"KiB", "MiB", "GiB", "TiB", "PiB"}[exp]
}

// Count renders a row count with thousands separators.
func Count(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// RowJSON renders formatted cells as one JSON object, columns in schema
// order. Nested and JSON values, numbers and booleans keep their JSON form;
// everything else is a string. Pass untruncated cells.
func RowJSON(cols []api.PreviewColumn, cells []api.Cell) string {
	var b strings.Builder
	b.WriteByte('{')
	for i, c := range cols {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(c.Name)
		b.Write(k)
		b.WriteByte(':')
		var cell api.Cell
		if i < len(cells) {
			cell = cells[i]
		} else {
			cell.Null = true
		}
		switch {
		case cell.Null:
			b.WriteString("null")
		case (c.Mode == "REPEATED" || c.Type == "RECORD" || c.Type == "STRUCT" || c.Type == "JSON") && json.Valid([]byte(cell.Text)):
			b.WriteString(cell.Text)
		case (c.Type == "INTEGER" || c.Type == "INT64" || c.Type == "FLOAT" || c.Type == "FLOAT64" || c.Type == "NUMERIC" || c.Type == "BIGNUMERIC") && json.Valid([]byte(cell.Text)):
			b.WriteString(cell.Text)
		case (c.Type == "BOOLEAN" || c.Type == "BOOL") && (cell.Text == "true" || cell.Text == "false"):
			b.WriteString(cell.Text)
		default:
			v, _ := json.Marshal(cell.Text)
			b.Write(v)
		}
	}
	b.WriteByte('}')
	return b.String()
}

// Age renders a duration coarsely for status text: 45m, 26h, 5d.
func Age(d time.Duration) string {
	switch {
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h"
	default:
		return strconv.Itoa(int(d.Hours()/24)) + "d"
	}
}
