// Package handoff renders the plain-text formats the TUI hands to an agent
// (spec 14.4): a table's schema, a failed query with its error, a sample of a
// result, a Run's summary. The text has no ANSI; a frontend only decides
// where it goes (the clipboard, a file).
package handoff

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/export"
	"github.com/davidsevcik/bigquery-tui/internal/format"
)

// SampleRows is how many rows a result sample holds.
const SampleRows = 20

// SampleFormat is the format of a result sample.
type SampleFormat string

const (
	Markdown SampleFormat = "markdown"
	TSV      SampleFormat = "tsv"
	NDJSON   SampleFormat = "ndjson"
)

// SchemaMarkdown is a table's schema as a compact markdown table: path, type
// and description, nested fields as dotted paths.
func SchemaMarkdown(s api.TableSchema) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## `%s` (%s", s.Table, s.Type)
	if s.Location != "" {
		fmt.Fprintf(&b, ", %s", s.Location)
	}
	b.WriteString(")\n\n")
	if s.Shard != nil {
		fmt.Fprintf(&b, "Date-sharded: `%s*` has %d tables, %s to %s (schema of the newest).\n\n", s.Shard.Prefix, s.Shard.Count, s.Shard.First, s.Shard.Last)
	}
	if s.Description != "" {
		b.WriteString(oneLine(s.Description) + "\n\n")
	}
	if s.NamesOnly {
		b.WriteString("Columns unknown (bqt could only read the table's name).\n")
		return b.String()
	}
	b.WriteString("| column | type | description |\n|---|---|---|\n")
	for _, c := range s.Columns {
		var notes []string
		if c.Required {
			notes = append(notes, "REQUIRED")
		}
		if c.Partition {
			notes = append(notes, "partitioning")
		}
		if c.Clustering > 0 {
			notes = append(notes, fmt.Sprintf("clustering %d", c.Clustering))
		}
		desc := cellText(c.Description)
		if len(notes) > 0 {
			desc = strings.TrimSpace("(" + strings.Join(notes, ", ") + ") " + desc)
		}
		fmt.Fprintf(&b, "| %s | %s | %s |\n", cellText(c.Name), cellText(c.Type), desc)
	}
	if s.ViewSQL != "" {
		fmt.Fprintf(&b, "\nView SQL:\n\n```sql\n%s\n```\n", strings.TrimRight(s.ViewSQL, "\n"))
	}
	return b.String()
}

// SchemaJSON is the schema in the shape `bqt schema` prints as data.
func SchemaJSON(s api.TableSchema) string {
	b, _ := json.MarshalIndent(s, "", "  ")
	return string(b) + "\n"
}

// QueryError is a failed query for an agent to fix: the SQL, the error with
// its position, the Execution project and the location.
func QueryError(sql string, qe api.QueryError, execProject, location string) string {
	var b strings.Builder
	b.WriteString("BigQuery query failed")
	if execProject != "" {
		fmt.Fprintf(&b, " (Execution project %s", execProject)
		if location != "" {
			fmt.Fprintf(&b, ", location %s", location)
		}
		b.WriteString(")")
	}
	b.WriteString(".\n\nError")
	if qe.Line > 0 {
		fmt.Fprintf(&b, " at line %d, column %d", qe.Line, qe.Col)
	}
	fmt.Fprintf(&b, ":\n\n    %s\n", strings.ReplaceAll(strings.TrimSpace(qe.Message), "\n", "\n    "))
	if qe.Hint != "" {
		fmt.Fprintf(&b, "\nHint: %s\n", qe.Hint)
	}
	fmt.Fprintf(&b, "\nSQL:\n\n```sql\n%s\n```\n", strings.TrimRight(sql, "\n"))
	if qe.Line > 0 {
		lines := strings.Split(sql, "\n")
		if qe.Line <= len(lines) {
			fmt.Fprintf(&b, "\nLine %d: `%s`\n", qe.Line, strings.TrimSpace(lines[qe.Line-1]))
		}
	}
	return b.String()
}

// Sample is the first rows of a result as markdown, TSV or NDJSON. cells
// should be unclipped so the values are exact.
func Sample(cols []api.PreviewColumn, rows [][]api.Cell, f SampleFormat) string {
	if len(rows) > SampleRows {
		rows = rows[:SampleRows]
	}
	switch f {
	case TSV:
		return export.TSV(cols, rows)
	case NDJSON:
		var b strings.Builder
		for _, r := range rows {
			b.WriteString(format.RowJSON(cols, r) + "\n")
		}
		return b.String()
	}
	var b strings.Builder
	b.WriteString("|")
	for _, c := range cols {
		fmt.Fprintf(&b, " %s |", cellText(c.Name))
	}
	b.WriteString("\n|")
	for range cols {
		b.WriteString("---|")
	}
	b.WriteString("\n")
	for _, r := range rows {
		b.WriteString("|")
		for i := range cols {
			v := "NULL"
			if i < len(r) && !r[i].Null {
				v = cellText(r[i].Text)
			}
			fmt.Fprintf(&b, " %s |", v)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// Run summarises a Run: SQL, estimate, state and outcome.
func Run(info api.RunInfo) string {
	var b strings.Builder
	fmt.Fprintf(&b, "BigQuery Run: %s", info.State)
	if info.ExecutionProject != "" {
		fmt.Fprintf(&b, " (Execution project %s", info.ExecutionProject)
		if info.Location != "" {
			fmt.Fprintf(&b, ", location %s", info.Location)
		}
		b.WriteString(")")
	}
	b.WriteString("\n\n")
	if e := info.Estimate; e != nil {
		fmt.Fprintf(&b, "- Estimate: %s %s", e.StatementType, format.Bytes(e.Bytes))
		if e.Accuracy != "" && e.Accuracy != "PRECISE" {
			fmt.Fprintf(&b, " (%s)", strings.ToLower(e.Accuracy))
		}
		if e.Capacity {
			b.WriteString(", on a reservation")
		} else if e.ApproxUSD != nil {
			fmt.Fprintf(&b, ", about $%.4f on demand", *e.ApproxUSD)
		}
		b.WriteString("\n")
	}
	if j := info.Job; j != nil {
		fmt.Fprintf(&b, "- Job: %s:%s.%s\n", j.Project, j.Location, j.ID)
	}
	if s := info.Stats; s != nil {
		fmt.Fprintf(&b, "- Processed %s", format.Bytes(s.BytesProcessed))
		if s.CacheHit {
			b.WriteString(" (cache hit)")
		}
		if s.SlotMs > 0 {
			fmt.Fprintf(&b, ", %d slot ms", s.SlotMs)
		}
		if s.AffectedRows > 0 {
			fmt.Fprintf(&b, ", %d rows affected", s.AffectedRows)
		}
		b.WriteString("\n")
	}
	if info.HasResult {
		fmt.Fprintf(&b, "- Result: %d rows, columns %s\n", info.TotalRows, columnList(info.Columns))
	}
	if info.Started != nil && info.Ended != nil {
		fmt.Fprintf(&b, "- Took %s\n", info.Ended.Sub(*info.Started).Round(1e6))
	}
	if e := info.Error; e != nil {
		b.WriteString("- Error")
		if e.Line > 0 {
			fmt.Fprintf(&b, " at line %d, column %d", e.Line, e.Col)
		}
		fmt.Fprintf(&b, ": %s\n", oneLine(e.Message))
	}
	if len(info.Params) > 0 {
		b.WriteString("- Parameters:")
		for _, p := range info.Params {
			v := p.Value
			if p.Null {
				v = "NULL"
			}
			fmt.Fprintf(&b, " @%s %s = %s;", p.Name, p.Type, v)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "\n```sql\n%s\n```\n", strings.TrimRight(info.SQL, "\n"))
	return b.String()
}

func columnList(cols []api.PreviewColumn) string {
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.Name + " " + c.Type
	}
	return strings.Join(names, ", ")
}

// cellText makes text safe inside a markdown table cell.
func cellText(s string) string {
	return strings.ReplaceAll(oneLine(s), "|", `\|`)
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
