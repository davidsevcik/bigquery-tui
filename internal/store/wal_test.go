package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	if _, err := c.ApplyUnit(ctx, bigUnit()); err != nil {
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

// A reader still in the WAL does not hold up the unit, which returns at once
// instead of waiting busy_timeout; the WAL is truncated in the background
// once the reader is gone, with no further write.
func TestApplyUnitTrimsTheWALAfterAReaderLeaves(t *testing.T) {
	oldEvery := walRetryEvery
	walRetryEvery = 20 * time.Millisecond
	t.Cleanup(func() { walRetryEvery = oldEvery })
	path := filepath.Join(t.TempDir(), "catalog.db")
	c, err := OpenCatalog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.SetMeta(ctx, "seed", "1"); err != nil {
		t.Fatal(err)
	}
	// another process reading, as the TUI's index load would
	other, err := OpenCatalogReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	rtx, err := other.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var v string
	if err := rtx.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = 'seed'").Scan(&v); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ApplyUnit(ctx, bigUnit()); err != nil {
		t.Fatal(err)
	}
	// the truncation gives up at once rather than holding the write lock
	// for busy_timeout (5 s) while the reader is there
	start := time.Now()
	if done, err := truncateWAL(ctx, c.db); done || err != nil {
		t.Fatalf("truncated past a reader: %v %v", done, err)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("the truncation waited %s for the reader", took)
	}
	if size := walBytes(t, path); size == 0 {
		t.Fatal("the reader must keep the WAL from being truncated while it reads")
	}
	if err := rtx.Rollback(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for walBytes(t, path) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the WAL is still %d bytes after the reader left", walBytes(t, path))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func walBytes(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

// bigUnit is one unit of 500 tables of 20 columns: about a MB of WAL.
func bigUnit() ApplyRequest {
	var tables []api.TableMeta
	for i := range 500 {
		cols := make([]string, 20)
		for j := range cols {
			cols[j] = fmt.Sprintf("column_with_a_long_name_%02d", j)
		}
		tables = append(tables, table("ds", fmt.Sprintf("t%04d", i), cols...))
	}
	return ApplyRequest{Unit: unit("p", "EU"), State: okState(t0), Datasets: []DatasetApply{{Meta: api.DatasetMeta{Project: "p", Name: "ds", Location: "EU"}, Tables: tables}}}
}
