package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

// A sync unit is written in one transaction, so the WAL grows to the size of
// the largest unit. After that transaction is checkpointed, the next write
// shrinks the file back to the journal size limit instead of keeping it at
// its peak (#91: a 12.8 GB WAL beside a 12.9 GB catalog).
func TestWALShrinksAfterALargeTransaction(t *testing.T) {
	old := walSizeLimit
	walSizeLimit = 1 << 20
	t.Cleanup(func() { walSizeLimit = old })
	path := filepath.Join(t.TempDir(), "catalog.db")
	c, err := OpenCatalog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	walSize := func() int64 {
		t.Helper()
		fi, err := os.Stat(path + "-wal")
		if err != nil {
			t.Fatal(err)
		}
		return fi.Size()
	}
	big := strings.Repeat("x", 4000)
	tx, err := c.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := range 4000 { // about 16 MB in one transaction
		if _, err := tx.Exec("INSERT INTO meta(key,value) VALUES(?,?)", fmt.Sprintf("k%05d", i), big); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if peak := walSize(); peak < 8<<20 {
		t.Fatalf("the large transaction left only %d bytes of WAL; the test needs a large one", peak)
	}
	if err := c.SetMeta(ctx, "after", "small"); err != nil {
		t.Fatal(err)
	}
	if got := walSize(); got > int64(walSizeLimit) {
		t.Fatalf("after the next write the WAL is %d bytes, want at most %d", got, walSizeLimit)
	}
}

// ApplyUnit leaves no large WAL behind on its own, without waiting for the
// next write: the TUI keeps the catalog open between syncs.
func TestApplyUnitTruncatesTheWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db")
	c, err := OpenCatalog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var tables []api.TableMeta
	for i := range 2000 {
		cols := make([]string, 20)
		for j := range cols {
			cols[j] = fmt.Sprintf("column_with_a_long_name_%02d", j)
		}
		tables = append(tables, table("ds", fmt.Sprintf("t%04d", i), cols...))
	}
	req := ApplyRequest{Unit: unit("p", "EU"), State: okState(t0), Datasets: []DatasetApply{{Meta: api.DatasetMeta{Project: "p", Name: "ds", Location: "EU"}, Tables: tables}}}
	if _, err := c.ApplyUnit(ctx, req); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() != 0 {
		t.Fatalf("the WAL is %d bytes after the unit, want it truncated", fi.Size())
	}
}
