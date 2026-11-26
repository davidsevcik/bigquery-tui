package handoff

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/davidsevcik/bigquery-tui/pkg/api"
)

type Kind string

const (
	KindSchemaMarkdown Kind = "schema-markdown"
	KindSchemaJSON     Kind = "schema-json"
	KindQueryError     Kind = "query-error"
	KindRowsMarkdown   Kind = "rows-markdown"
	KindRowsTSV        Kind = "rows-tsv"
	KindRowsNDJSON     Kind = "rows-ndjson"
	KindRun            Kind = "run"
)

const dir = ".bqt-handoff"

type Situation struct {
	Table *api.ObjectPath
	Run   *api.RunInfo
}

type Item struct {
	Kind  Kind
	Label string
	File  string
	Table api.ObjectPath
	Run   api.RunInfo
}

func (it Item) RowsFormat() SampleFormat {
	switch it.Kind {
	case KindRowsMarkdown:
		return Markdown
	case KindRowsTSV:
		return TSV
	case KindRowsNDJSON:
		return NDJSON
	}
	return ""
}

func (it Item) DefaultPath() string { return filepath.Join(".", dir, it.File) }

var unsafeFile = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func Items(s Situation) []Item {
	var items []Item
	if p := s.Table; p != nil && p.Table != "" {
		base := unsafeFile.ReplaceAllString(p.String(), "_")
		items = append(items,
			Item{Kind: KindSchemaMarkdown, Label: "Schema of " + p.String() + " (markdown)", File: base + ".md", Table: *p},
			Item{Kind: KindSchemaJSON, Label: "Schema of " + p.String() + " (JSON, as bqt schema)", File: base + ".json", Table: *p})
	}
	r := s.Run
	if r == nil {
		return items
	}
	if r.Error != nil {
		items = append(items, Item{Kind: KindQueryError, Label: fmt.Sprintf("Query and error of run #%d", r.ID), File: "query-error.md", Run: *r})
	}
	if r.HasResult {
		for _, f := range []struct {
			kind Kind
			ext  string
		}{{KindRowsMarkdown, "md"}, {KindRowsTSV, "tsv"}, {KindRowsNDJSON, "ndjson"}} {
			items = append(items, Item{
				Kind: f.kind, Label: fmt.Sprintf("First %d rows of run #%d (%s)", SampleRows, r.ID, f.ext),
				File: fmt.Sprintf("sample-%d.%s", r.ID, f.ext), Run: *r,
			})
		}
	}
	return append(items, Item{Kind: KindRun, Label: fmt.Sprintf("Run #%d: SQL, estimate, state, outcome", r.ID), File: fmt.Sprintf("run-%d.md", r.ID), Run: *r})
}

type Need int

const (
	NeedRun Need = iota
	NeedTable
	NeedRunError
	NeedRunRows
)

func Missing(k Kind) Need {
	switch k {
	case KindSchemaMarkdown, KindSchemaJSON:
		return NeedTable
	case KindQueryError:
		return NeedRunError
	case KindRowsMarkdown, KindRowsTSV, KindRowsNDJSON:
		return NeedRunRows
	}
	return NeedRun
}

func Write(path, text string, overwrite bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if overwrite {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return err
	}
	_, err = f.WriteString(text)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
