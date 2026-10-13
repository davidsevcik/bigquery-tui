// Package export writes a query result to a file format. It streams: rows
// are pulled page by page and written as they arrive, so the size of a
// result is bounded by the disk, not by memory.
package export

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq"
	"github.com/davidsevcik/bigquery-tui/internal/format"
)

// Format is an export file format.
type Format string

const (
	CSV    Format = "csv"
	JSON   Format = "json" // one JSON array of objects
	NDJSON Format = "ndjson"
)

// Formats lists the formats in the order a frontend should offer them.
var Formats = []Format{CSV, JSON, NDJSON}

// FormatOf picks the format from a file name; unknown extensions are CSV.
func FormatOf(path string) Format {
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(path), ".")) {
	case "json":
		return JSON
	case "ndjson", "jsonl":
		return NDJSON
	}
	return CSV
}

// ParseFormat validates a format name.
func ParseFormat(s string) (Format, error) {
	switch f := Format(strings.ToLower(s)); f {
	case CSV, JSON, NDJSON:
		return f, nil
	case "jsonl":
		return NDJSON, nil
	}
	return "", fmt.Errorf("unknown export format %q (csv, json, ndjson)", s)
}

// Next returns the rows from startIndex on (any number of them); no rows
// means the end.
type Next func(ctx context.Context, startIndex int64) ([][]any, error)

// Write streams every row to w. progress is called after each batch with the
// number of rows written so far. It returns the row count.
func Write(ctx context.Context, w io.Writer, f Format, schema []bq.Field, next Next, progress func(rows int64)) (int64, error) {
	bw := bufio.NewWriterSize(w, 1<<16)
	var cw *csv.Writer
	switch f {
	case CSV:
		cw = csv.NewWriter(bw)
		head := make([]string, len(schema))
		for i, c := range schema {
			head[i] = c.Name
		}
		if err := cw.Write(head); err != nil {
			return 0, err
		}
	case JSON:
		bw.WriteString("[")
	}
	names := make([][]byte, len(schema))
	for i, c := range schema {
		names[i], _ = json.Marshal(c.Name)
	}
	var n int64
	rec := make([]string, len(schema))
	for {
		if err := ctx.Err(); err != nil {
			return n, err
		}
		rows, err := next(ctx, n)
		if err != nil {
			return n, err
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			switch f {
			case CSV:
				for i, c := range schema {
					rec[i] = ""
					if i < len(row) {
						rec[i], _ = format.Text(c, row[i])
					}
				}
				if err := cw.Write(rec); err != nil {
					return n, err
				}
			default:
				if f == JSON {
					if n > 0 {
						bw.WriteString(",")
					}
					bw.WriteString("\n  ")
				}
				bw.WriteByte('{')
				for i, c := range schema {
					if i > 0 {
						bw.WriteByte(',')
					}
					bw.Write(names[i])
					bw.WriteByte(':')
					var v any
					if i < len(row) {
						v = row[i]
					}
					bw.WriteString(format.JSONValue(c, v))
				}
				bw.WriteByte('}')
				if f == NDJSON {
					bw.WriteByte('\n')
				}
			}
			n++
		}
		if progress != nil {
			progress(n)
		}
	}
	switch f {
	case CSV:
		cw.Flush()
		if err := cw.Error(); err != nil {
			return n, err
		}
	case JSON:
		if n > 0 {
			bw.WriteString("\n")
		}
		bw.WriteString("]\n")
	}
	return n, bw.Flush()
}

// TSV renders formatted cells for the clipboard: a header line and one line
// per row, tab-separated. Tabs and line breaks inside values become spaces.
func TSV(cols []api.PreviewColumn, rows [][]api.Cell) string {
	clean := strings.NewReplacer("\t", " ", "\n", " ", "\r", " ")
	var b strings.Builder
	for i, c := range cols {
		if i > 0 {
			b.WriteByte('\t')
		}
		b.WriteString(clean.Replace(c.Name))
	}
	b.WriteByte('\n')
	for _, r := range rows {
		for i := range cols {
			if i > 0 {
				b.WriteByte('\t')
			}
			if i < len(r) && !r[i].Null {
				b.WriteString(clean.Replace(r[i].Text))
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}
