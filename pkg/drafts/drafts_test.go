package drafts

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/davidsevcik/bigquery-tui/pkg/api"
)

func exists(t *testing.T, p string) bool {
	t.Helper()
	_, err := os.Stat(p)
	return err == nil
}

func TestDirIsPerFrontend(t *testing.T) {
	if got := Dir("/data/drafts", api.SourceTUI); got != filepath.Join("/data/drafts", "tui") {
		t.Fatal(got)
	}
	if got := Dir("/data/drafts", api.SourceGUI); got != filepath.Join("/data/drafts", "gui") {
		t.Fatal(got)
	}
}

func TestADraftIsHeldByOneInstance(t *testing.T) {
	s := In(filepath.Join(t.TempDir(), "tui")) // created on the first Hold
	d, err := s.Hold("a1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Hold("a1"); !errors.Is(err, ErrHeld) {
		t.Fatalf("a held draft must not be held twice: %v", err)
	}
	d.Release()
	d2, err := s.Hold("a1")
	if err != nil {
		t.Fatalf("a released draft can be held again: %v", err)
	}
	d2.Release()
}

func TestSaveWritesTheDraftAndEmptyTextRemovesIt(t *testing.T) {
	s := In(t.TempDir())
	d, err := s.Hold(NewID())
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Save("select 1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.read(d.id); got != "select 1" {
		t.Fatalf("%q", got)
	}
	if exists(t, s.Path(d.id)+".tmp") {
		t.Fatal("the temporary file must be renamed")
	}
	if err := d.Save(""); err != nil {
		t.Fatal(err)
	}
	if exists(t, s.Path(d.id)) {
		t.Fatal("an empty buffer has no draft")
	}
	// a write that fails leaves the draft as it was and reports the error
	if err := d.Save("select 2"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(s.Path(d.id)+".tmp", "keep"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := d.Save("select 3"); err == nil {
		t.Fatal("a failed write must be reported")
	}
	if got, _ := s.read(d.id); got != "select 2" {
		t.Fatalf("%q", got)
	}
}

func TestReleaseKeepsTheDraftAndDiscardDeletesIt(t *testing.T) {
	s := In(t.TempDir())
	kept, _ := s.Hold("kept")
	_ = kept.Save("select 'kept'")
	kept.Release()
	if !exists(t, s.Path("kept")) || !exists(t, s.lockPath("kept")) {
		t.Fatal("a released draft stays for restore")
	}

	empty, _ := s.Hold("empty")
	empty.Release()
	if exists(t, s.lockPath("empty")) {
		t.Fatal("an empty buffer leaves no lock file")
	}

	gone, _ := s.Hold("gone")
	_ = gone.Save("select 'gone'")
	gone.Discard()
	if exists(t, s.Path("gone")) || exists(t, s.lockPath("gone")) {
		t.Fatal("a discarded draft is deleted with its lock")
	}
}

func TestOrphansListsUnheldDraftsNewestFirstAndCleansUp(t *testing.T) {
	dir := t.TempDir()
	s := In(dir)
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	write := func(id, text string, age time.Duration) {
		t.Helper()
		if err := os.WriteFile(s.Path(id), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		at := now.Add(-age)
		if err := os.Chtimes(s.Path(id), at, at); err != nil {
			t.Fatal(err)
		}
	}
	write("older", "\n  select 'older'\nfrom t", 2*time.Hour)
	write("newer", "select 'newer'", time.Hour)
	write("blank", "  \n\t", time.Hour)
	write("stale", "select 'stale'", maxAge+time.Hour)
	write("mine", "select 'mine'", time.Hour)
	write("held", "select 'held'", time.Hour)
	held, err := s.Hold("held")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	if err := os.WriteFile(s.lockPath("lone"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	got := s.Orphans(now, []string{"mine"})
	if len(got) != 2 || got[0].ID != "newer" || got[1].ID != "older" {
		t.Fatalf("%+v", got)
	}
	if o := got[1]; o.First != "select 'older'" || o.Lines != 3 || !o.ModTime.Equal(now.Add(-2*time.Hour)) {
		t.Fatalf("%+v", o)
	}
	for _, id := range []string{"blank", "stale"} {
		if exists(t, s.Path(id)) || exists(t, s.lockPath(id)) {
			t.Fatalf("%s must be deleted", id)
		}
	}
	if exists(t, s.lockPath("lone")) {
		t.Fatal("a lock file without a draft is left over from an empty buffer")
	}
	if !exists(t, s.Path("mine")) || !exists(t, s.Path("held")) {
		t.Fatal("open and held drafts stay")
	}
	if In(filepath.Join(dir, "missing")).Orphans(now, nil) != nil {
		t.Fatal("no directory: no drafts")
	}
}

func TestRestoreTakesOverAnOrphan(t *testing.T) {
	s := In(t.TempDir())
	if _, _, err := s.Restore("none"); err == nil {
		t.Fatal("a missing draft cannot be restored")
	}
	d, _ := s.Hold("x")
	_ = d.Save("select 'x'")
	if _, _, err := s.Restore("x"); !errors.Is(err, ErrHeld) {
		t.Fatalf("a held draft cannot be restored: %v", err)
	}
	d.Release()
	r, text, err := s.Restore("x")
	if err != nil || text != "select 'x'" {
		t.Fatalf("%q %v", text, err)
	}
	r.Release()
	if !exists(t, s.lockPath("x")) {
		t.Fatal("a restored draft with text keeps its lock file on release")
	}
}

func TestRestorePassesOtherLockErrorsThrough(t *testing.T) {
	s := In(t.TempDir())
	if err := os.WriteFile(s.Path("x"), []byte("select 'x'"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(s.lockPath("x"), 0o700); err != nil { // the lock file cannot be opened
		t.Fatal(err)
	}
	if _, _, err := s.Restore("x"); err == nil || errors.Is(err, ErrHeld) {
		t.Fatalf("only a held lock is ErrHeld: %v", err)
	}
}
