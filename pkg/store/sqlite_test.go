package store

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	waitForARollbackBehindTheCallersBack(tx, 100*time.Millisecond)
	stmt, err := tx.PrepareContext(context.Background(), "INSERT INTO meta(key, value) VALUES(?, ?)")
	if err != nil {
		t.Fatalf("prepare after the caller's cancel: %v", err)
	}
	defer stmt.Close()
	if _, err := stmt.ExecContext(context.Background(), "k", "v"); err != nil {
		t.Fatalf("exec after the caller's cancel: %v", err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatalf("commit after the caller's cancel: %v", err)
	}
	if v, ok, err := c.Meta(ctx, "k"); err != nil || !ok || v != "v" {
		t.Fatalf("committed row = %q, %v, %v", v, ok, err)
	}
}

func waitForARollbackBehindTheCallersBack(tx *storeTx, limit time.Duration) {
	for deadline := time.Now().Add(limit); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if _, err := tx.ExecContext(context.Background(), "SELECT 1"); err != nil {
			return
		}
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
	if err := tx.Commit(cctx); !errors.Is(err, context.Canceled) {
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

func TestACancelEndsTheWaitForAPooledConnection(t *testing.T) {
	c := openTemp(t)
	for range c.db.Stats().MaxOpenConnections {
		held, err := c.db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer held.Close()
	}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	waitsBefore := c.db.Stats().WaitCount
	done := make(chan error, 1)
	go func() {
		_, err := c.LoadIndex(cctx)
		done <- err
	}()
	for c.db.Stats().WaitCount == waitsBefore {
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("LoadIndex cancelled while waiting for a connection returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("LoadIndex kept waiting for a connection after its caller cancelled")
	}
}

func TestATransactionGivesItsConnectionBack(t *testing.T) {
	c := openTemp(t)
	for _, end := range []struct {
		name string
		end  func(*storeTx) error
	}{
		{"commit", func(tx *storeTx) error { return tx.Commit(ctx) }},
		{"rollback", func(*storeTx) error { return nil }},
	} {
		tx, err := beginTx(ctx, c.db, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := end.end(tx); err != nil {
			t.Fatal(err)
		}
		tx.Rollback()
		if n := c.db.Stats().InUse; n != 0 {
			t.Fatalf("after %s: connections in use = %d", end.name, n)
		}
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
			if !ok || fd.Body == nil || fd.Name.Name == "beginTx" {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "BeginTx" {
						t.Errorf("%s: %s calls BeginTx, begin through beginTx", fset.Position(call.Pos()), fd.Name.Name)
					}
				}
				return true
			})
		}
	}
	if scanned == 0 {
		t.Fatal("no store sources found")
	}
}
