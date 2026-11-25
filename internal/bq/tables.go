package bq

import (
	"context"
	"fmt"
	"time"

	bqv2 "google.golang.org/api/bigquery/v2"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

type TableRef struct {
	Project, Dataset, Table string
}

func (r TableRef) String() string { return r.Project + "." + r.Dataset + "." + r.Table }

type Field struct {
	Name   string
	Type   string
	Mode   string
	Fields []Field
}

type TableInfo struct {
	Type         api.TableType
	Rows         int64
	Bytes        int64
	LastModified time.Time
	Schema       []Field
}

type RawPage struct {
	Rows      [][]any
	TotalRows int64
}

type Tables interface {
	GetTable(ctx context.Context, ref TableRef) (TableInfo, error)
	ListRows(ctx context.Context, ref TableRef, startIndex int64, maxRows int) (RawPage, error)
}

func (c *Client) GetTable(ctx context.Context, ref TableRef) (TableInfo, error) {
	t, err := c.svc.Tables.Get(ref.Project, ref.Dataset, ref.Table).Context(ctx).Do()
	if err != nil {
		return TableInfo{}, Wrap("get table "+ref.String(), err)
	}
	info := TableInfo{Type: api.ParseTableType(t.Type), Rows: int64(t.NumRows), Bytes: t.NumBytes}
	if t.LastModifiedTime > 0 {
		info.LastModified = time.UnixMilli(int64(t.LastModifiedTime)).UTC()
	}
	if t.Schema != nil {
		info.Schema = convertFields(t.Schema.Fields)
	}
	return info, nil
}

func convertFields(fs []*bqv2.TableFieldSchema) []Field {
	out := make([]Field, 0, len(fs))
	for _, f := range fs {
		out = append(out, Field{Name: f.Name, Type: f.Type, Mode: f.Mode, Fields: convertFields(f.Fields)})
	}
	return out
}

func (c *Client) ListRows(ctx context.Context, ref TableRef, startIndex int64, maxRows int) (RawPage, error) {
	call := c.svc.Tabledata.List(ref.Project, ref.Dataset, ref.Table).Context(ctx).
		StartIndex(uint64(startIndex)).MaxResults(int64(maxRows)).FormatOptionsUseInt64Timestamp(true)
	r, err := call.Do()
	if err != nil {
		return RawPage{}, Wrap("list rows of "+ref.String(), err)
	}
	page := RawPage{TotalRows: r.TotalRows}
	for _, row := range r.Rows {
		cells := make([]any, len(row.F))
		for i, cell := range row.F {
			cells[i] = cell.V
		}
		page.Rows = append(page.Rows, cells)
	}
	return page, nil
}

var ErrNeedsQuery = fmt.Errorf("this object can only be read with a query")
