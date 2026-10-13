package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHistoryCreateFTSAndRecent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	h, err := OpenHistory(path)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if v, _ := h.Version(); v != HistorySchemaVersion {
		t.Fatalf("version %d", v)
	}
	_, err = h.DB().Exec(`INSERT INTO history(project,location,job_id,execution_project,sql,state,created_at,source) VALUES('p','EU','j1','exec','SELECT revenue FROM orders','DONE',1,'tui')`)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := h.DB().QueryRow(`SELECT COUNT(*) FROM history_fts WHERE history_fts MATCH 'revenue'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("fts: %d %v", n, err)
	}
	if _, err := h.DB().Exec(`INSERT INTO history(project,location,job_id,execution_project,sql,state,created_at,source) VALUES('p','EU','j1','exec','x','DONE',2,'tui')`); err == nil {
		t.Fatal("(project, location, job_id) must be unique")
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h.TouchRecent(ctx, "p.d.a", "table", base)
	h.TouchRecent(ctx, "p.d.b", "table", base.Add(time.Minute))
	h.TouchRecent(ctx, "p.d.a", "table", base.Add(2*time.Minute))
	rec, _ := h.Recent(ctx, 10)
	if len(rec) != 2 || rec[0] != "p.d.a" {
		t.Fatalf("%v", rec)
	}
}

func TestHistoryMigrationBacksUp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	v1 := []string{"CREATE TABLE a(x INTEGER); INSERT INTO a VALUES (1);"}
	h, err := openHistory(path, v1, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	h.Close()
	v2 := append(append([]string{}, v1...), "ALTER TABLE a ADD COLUMN y INTEGER DEFAULT 7;")
	h, err = openHistory(path, v2, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if !strings.Contains(h.Notice, "v1 to v2") {
		t.Fatalf("notice %q", h.Notice)
	}
	var y int
	if err := h.DB().QueryRow("SELECT y FROM a").Scan(&y); err != nil || y != 7 {
		t.Fatalf("%d %v", y, err)
	}
	if _, err := os.Stat(path + ".bak-v1"); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	// A further migration keeps only the newest backup.
	h.Close()
	v3 := append(append([]string{}, v2...), "ALTER TABLE a ADD COLUMN z INTEGER DEFAULT 9;")
	h, err = openHistory(path, v3, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".bak-v2"); err != nil {
		t.Fatal("bak-v2 expected")
	}
	if _, err := os.Stat(path + ".bak-v1"); err == nil {
		t.Fatal("older backup must be removed")
	}
}

func TestHistoryMigrationFailureRollsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	v1 := []string{"CREATE TABLE a(x INTEGER);"}
	h, _ := openHistory(path, v1, time.Now)
	h.Close()
	bad := append(append([]string{}, v1...), "ALTER TABLE a ADD COLUMN ok INTEGER; THIS IS NOT SQL;")
	if _, err := openHistory(path, bad, time.Now); err == nil {
		t.Fatal("expected migration error")
	}
	h, err := openHistory(path, v1, time.Now)
	if err != nil {
		t.Fatalf("file must still be at v1: %v", err)
	}
	defer h.Close()
	if _, err := h.DB().Exec("SELECT ok FROM a"); err == nil {
		t.Fatal("failed migration must have been rolled back")
	}
}

func TestHistoryNewerThanBinaryRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	v2 := []string{"CREATE TABLE a(x INTEGER);", "ALTER TABLE a ADD COLUMN y INTEGER;"}
	h, _ := openHistory(path, v2, time.Now)
	h.Close()
	_, err := openHistory(path, v2[:1], time.Now)
	if !errors.Is(err, ErrHistoryTooNew) {
		t.Fatalf("%v", err)
	}
	if _, serr := os.Stat(path); serr != nil {
		t.Fatal("a too-new file must be left alone")
	}
}

func TestHistoryCorruptIsMovedAside(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.db")
	os.WriteFile(path, []byte("garbage garbage garbage garbage garbage garbage garbage garbage garbage"), 0o600)
	now := time.Date(2026, 10, 13, 9, 8, 7, 0, time.UTC)
	h, err := openHistory(path, historyMigrations, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if !strings.Contains(h.Notice, "corrupt") || !strings.Contains(h.Notice, "20261013T090807Z") {
		t.Fatalf("notice %q", h.Notice)
	}
	if _, err := os.Stat(path + ".corrupt-20261013T090807Z"); err != nil {
		t.Fatalf("corrupt file must be preserved: %v", err)
	}
	if v, _ := h.Version(); v != HistorySchemaVersion {
		t.Fatal("fresh history expected")
	}
}
