package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"modernc.org/sqlite"
)

func TestACancelledCallerDoesNotEndItsTransaction(t *testing.T) {
	c := openTemp(t)
	cctx, cancel := context.WithCancel(ctx)
	tx, err := beginTx(cctx, c.db, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	cancel()
	waitForARollbackBehindTheCallersBack(c.db, 100*time.Millisecond)
	stmt, err := tx.PrepareContext(context.Background(), "INSERT INTO meta(key, value) VALUES(?, ?)")
	if err != nil {
		t.Fatalf("prepare after the caller's cancel: %v", err)
	}
	defer stmt.Close()
	if _, err := stmt.ExecContext(context.Background(), "k", "v"); err != nil {
		t.Fatalf("exec after the caller's cancel: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit after the caller's cancel: %v", err)
	}
	if v, ok, err := c.Meta(ctx, "k"); err != nil || !ok || v != "v" {
		t.Fatalf("committed row = %q, %v, %v", v, ok, err)
	}
}

func waitForARollbackBehindTheCallersBack(db *sql.DB, limit time.Duration) {
	for deadline := time.Now().Add(limit); time.Now().Before(deadline) && db.Stats().InUse > 0; {
		time.Sleep(time.Millisecond)
	}
}

func TestATransactionsStatementsStillStopOnTheCallersCancel(t *testing.T) {
	c := openTemp(t)
	cctx, cancel := context.WithCancel(ctx)
	tx, err := beginTx(cctx, c.db, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	cancel()
	if _, err := tx.ExecContext(cctx, "INSERT INTO meta(key, value) VALUES('k', 'v')"); !errors.Is(err, context.Canceled) {
		t.Fatalf("exec on the cancelled context: %v", err)
	}
	if err := commitTx(cctx, tx); !errors.Is(err, context.Canceled) {
		t.Fatalf("commit on the cancelled context: %v", err)
	}
	if _, ok, _ := c.Meta(ctx, "k"); ok {
		t.Fatal("a cancelled transaction must not commit")
	}
}

func TestBeginTxOnACancelledContextTakesNoConnection(t *testing.T) {
	c := openTemp(t)
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := beginTx(cctx, c.db, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("begin on a cancelled context: %v", err)
	}
	if n := c.db.Stats().InUse; n != 0 {
		t.Fatalf("connections in use = %d", n)
	}
}

var (
	registerTestCancel sync.Once
	testCancel         atomic.Pointer[context.CancelFunc]
)

func openTempCancellable(t *testing.T) *Catalog {
	t.Helper()
	registerTestCancel.Do(func() {
		sqlite.MustRegisterScalarFunction("test_cancel", 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
			if cancel := testCancel.Load(); cancel != nil {
				(*cancel)()
			}
			return nil, nil
		})
	})
	return openTemp(t)
}

func cancelInTrigger(t *testing.T, c *Catalog, event string, cancel context.CancelFunc) {
	t.Helper()
	if _, err := c.db.Exec("CREATE TRIGGER test_cancel " + event + " BEGIN SELECT test_cancel(); END"); err != nil {
		t.Fatal(err)
	}
	testCancel.Store(&cancel)
	t.Cleanup(func() { testCancel.Store(nil) })
}

func TestApplyUnitCancelledBetweenStatementsLeavesOldData(t *testing.T) {
	for _, tc := range []struct{ name, event string }{
		{"while writing its first new table", "AFTER INSERT ON tables"},
		{"while writing the unit state, its last statement", "AFTER UPDATE ON sync_units"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := openTempCancellable(t)
			apply(t, c, unit("p", "EU"), t0, dsApply("d1", "EU", table("d1", "a", "x")))
			cctx, cancel := context.WithCancel(ctx)
			defer cancel()
			cancelInTrigger(t, c, tc.event, cancel)
			_, err := c.ApplyUnit(cctx, ApplyRequest{Unit: unit("p", "EU"), State: okState(t0.Add(time.Hour)), Datasets: []DatasetApply{dsApply("d1", "EU", table("d1", "zzz", "y"))}})
			if cctx.Err() == nil {
				t.Fatal("the trigger never cancelled the apply")
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled apply returned %v", err)
			}
			assertOldSnapshot(t, c)
		})
	}
}

func assertOldSnapshot(t *testing.T, c *Catalog) {
	t.Helper()
	tabs, err := c.Tables(ctx, "p", "d1")
	if err != nil || len(tabs) != 1 || tabs[0].Name != "a" {
		t.Fatalf("old tables must remain: %+v, %v", tabs, err)
	}
	cols, err := c.Columns(ctx, "p", "d1", "a")
	if err != nil || len(cols) != 1 || cols[0].FieldPath != "x" {
		t.Fatalf("old columns must remain: %+v, %v", cols, err)
	}
	units, err := c.Units(ctx)
	if err != nil || len(units) != 1 || units[0].LastSuccess == nil || !units[0].LastSuccess.Equal(t0) {
		t.Fatalf("old unit state must remain: %+v, %v", units, err)
	}
}

func TestNoStoreTransactionBeginsOnTheCallersContext(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	scanned := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		scanned++
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			calls := calledNames(fset, fd.Body)
			if pos, ok := calls["BeginTx"]; ok && fd.Name.Name != "beginTx" {
				t.Errorf("%s: %s calls BeginTx, begin through beginTx", pos, fd.Name.Name)
			}
			if pos, ok := calls["Commit"]; ok && calls["beginTx"] != "" && fd.Name.Name != "commitTx" {
				t.Errorf("%s: %s commits a beginTx transaction with Commit, commit through commitTx", pos, fd.Name.Name)
			}
		}
	}
	if scanned == 0 {
		t.Fatal("no store sources found")
	}
}

func calledNames(fset *token.FileSet, body *ast.BlockStmt) map[string]string {
	calls := map[string]string{}
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		var name string
		switch fn := call.Fun.(type) {
		case *ast.SelectorExpr:
			name = fn.Sel.Name
		case *ast.Ident:
			name = fn.Name
		}
		if _, seen := calls[name]; name != "" && !seen {
			calls[name] = fset.Position(call.Pos()).String()
		}
		return true
	})
	return calls
}
