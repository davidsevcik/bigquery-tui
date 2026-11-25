package handoff

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/davidsevcik/bigquery-tui/pkg/api"
)

// Kind is one kind of hand-off.
type Kind string

const (
	KindSchemaMarkdown Kind = "schema-markdown" // the selected table's schema as markdown
	KindSchemaJSON     Kind = "schema-json"     // the same, as `bqt schema` prints it
	KindQueryError     Kind = "query-error"     // the focused Run's SQL and error
	// The focused Run's first rows. Not a Sample: a Sample is a table's
	// rows read without a Run.
	KindRowsMarkdown Kind = "rows-markdown"
	KindRowsTSV      Kind = "rows-tsv"
	KindRowsNDJSON   Kind = "rows-ndjson"
	KindRun          Kind = "run" // the focused Run's SQL, estimate, state and outcome
)

// dir is where hand-off files go by default, relative to the working
// directory, so that an agent started there finds them.
const dir = ".bqt-handoff"

// Situation is what a hand-off can be about: the table or Shard family
// selected in the Explorer, and the focused Run. nil means none.
type Situation struct {
	Table *api.ObjectPath
	Run   *api.RunInfo
}

// Item is one hand-off on offer: its kind, the menu label, the default file
// name and what it is about. engine.Engine.Handoff renders it.
type Item struct {
	Kind  Kind
	Label string
	File  string
	Table api.ObjectPath // the schema kinds
	Run   api.RunInfo    // the Run kinds, as the frontend shows it
}

// RowsFormat is the format of a rows kind ("" for the others).
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

// DefaultPath is where Write puts the item unless the user names another
// file: .bqt-handoff/File under the working directory.
func (it Item) DefaultPath() string { return filepath.Join(".", dir, it.File) }

var unsafeFile = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// Items lists what can be handed off in situation s, in menu order: the
// table's schema (markdown, JSON), then for the focused Run its error when
// it failed, its first rows when it has a result, and its summary. The file
// names keep each kind apart, so one item never overwrites another's file.
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

// Need is what an item kind needs to be on offer.
type Need int

const (
	NeedRun      Need = iota // a focused Run
	NeedTable                // a table or Shard family selected
	NeedRunError             // a focused Run that failed
	NeedRunRows              // a focused Run with result rows
)

// Missing is what kind k needs, so that a frontend can say why it is not on
// offer.
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

// Write writes a hand-off's text to path, making its folder. Without
// overwrite the file is created exclusively: one that appeared since the
// user was asked is not replaced, and the error is os.ErrExist (errors.Is).
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
