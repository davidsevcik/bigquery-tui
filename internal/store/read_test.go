package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

func seed(t *testing.T, c *Catalog) {
	t.Helper()
	orders := table("sales", "orders", "id", "customer_address", "customer_address.city")
	orders.Description = "All orders"
	orders.Columns[1].Description = "Shipping ADDRESS of the buyer"
	items := table("sales", "items", "sku", "qty")
	logs := table("logs", "requests", "id", "url")
	apply(t, c, unit("p", "EU"), t0,
		DatasetApply{Meta: api.DatasetMeta{Project: "p", Name: "sales", Location: "EU", Description: "Sales data"}, Tables: []api.TableMeta{orders, items}},
		dsApply("logs", "EU", logs))
}

func TestEachColumn(t *testing.T) {
	c := openTemp(t)
	seed(t, c)
	type row struct {
		id         int64
		path, desc string
	}
	var got []row
	if err := c.EachColumn(ctx, func(id int64, p, d string) { got = append(got, row{id, p, d}) }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 7 {
		t.Fatalf("%d rows", len(got))
	}
	var seenDesc bool
	for i, r := range got {
		if i > 0 && r.id < got[i-1].id {
			t.Fatal("rows must be grouped by table")
		}
		if r.desc == "shipping address of the buyer" {
			seenDesc = true
		}
	}
	if !seenDesc {
		t.Fatalf("descriptions are lower-cased: %+v", got)
	}
}

func TestLoadIndex(t *testing.T) {
	c := openTemp(t)
	seed(t, c)
	shard := table("logs", "ev_20240101", "x")
	shard.Shard = &api.ShardFamily{Prefix: "ev_", First: "20240101", Last: "20240102", Count: 2}
	apply(t, c, unit("p", "US"), t0, dsApply("us", "US", shard))
	idx, err := c.LoadIndex(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Projects) != 1 || len(idx.Datasets) != 3 || len(idx.Tables) != 4 || len(idx.Units) != 2 {
		t.Fatalf("%d %d %d %d", len(idx.Projects), len(idx.Datasets), len(idx.Tables), len(idx.Units))
	}
	var sales IndexDataset
	for _, d := range idx.Datasets {
		if d.Name == "sales" {
			sales = d
		}
	}
	if sales.Description != "Sales data" || sales.Project != "p" || sales.Location != "EU" {
		t.Fatalf("%+v", sales)
	}
	var found bool
	for _, tb := range idx.Tables {
		if tb.Name == "orders" {
			found = true
			if tb.Dataset != "sales" || tb.DatasetID != sales.ID || tb.Description != "All orders" || len(tb.Columns) != 0 {
				t.Fatalf("%+v", tb)
			}
			cols, _ := c.ColumnsByTableID(ctx, tb.ID)
			if len(cols) != 3 || cols[0].FieldPath != "id" {
				t.Fatalf("%+v", cols)
			}
		}
		if tb.Name == "ev_20240101" && (tb.Shard == nil || tb.Shard.Count != 2) {
			t.Fatalf("%+v", tb)
		}
	}
	if !found {
		t.Fatal("orders missing")
	}
}

func TestWatcherSeesOtherConnectionsAndProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db")
	writer, _ := OpenCatalog(path)
	defer writer.Close()
	reader, err := OpenCatalogReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	w, err := reader.NewWatcher(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if ch, _ := w.Changed(ctx); ch {
		t.Fatal("nothing changed yet")
	}
	apply(t, writer, unit("p", "EU"), t0, dsApply("d", "EU", table("d", "t", "c")))
	if ch, _ := w.Changed(ctx); !ch {
		t.Fatal("a write through another handle must be seen")
	}
	if ch, _ := w.Changed(ctx); ch {
		t.Fatal("no further change")
	}
	// same handle, different pooled connection
	w2, _ := writer.NewWatcher(ctx)
	defer w2.Close()
	apply(t, writer, unit("p", "EU"), t0.Add(time.Hour), dsApply("d", "EU", table("d", "t2", "c")))
	if ch, _ := w2.Changed(ctx); !ch {
		t.Fatal("a write on the same handle must be seen")
	}
}
