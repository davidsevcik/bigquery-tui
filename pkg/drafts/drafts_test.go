package drafts

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/davidsevcik/bigquery-tui/pkg/api"
)

func wantOneOrphanUnder(t *testing.T, s *Store, now time.Time, project string) {
	t.Helper()
	o := s.Orphans(now, nil)
	if len(o) != 1 {
		t.Fatalf("want one orphan, got %d: %+v", len(o), o)
	}
	if o[0].Execution != project {
		t.Fatalf("want the orphan's Execution project %q, got %q", project, o[0].Execution)
	}
}

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
	s := In(filepath.Join(t.TempDir(), "tui"))
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
	if err := os.Mkdir(s.lockPath("x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Restore("x"); err == nil || errors.Is(err, ErrHeld) {
		t.Fatalf("only a held lock is ErrHeld: %v", err)
	}
}

func TestADraftRecordsItsExecutionProject(t *testing.T) {
	s := In(t.TempDir())
	now := time.Now()
	d, err := s.Hold("x")
	if err != nil {
		t.Fatal(err)
	}
	if d.Execution() != "" {
		t.Fatalf("a new draft has no record: %q", d.Execution())
	}
	if err := d.SetExecution("win-proj"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Hold("x"); !errors.Is(err, ErrHeld) {
		t.Fatalf("recording the project keeps the draft held: %v", err)
	}
	if err := d.Save("select 1"); err != nil {
		t.Fatal(err)
	}
	d.Release()
	if got, _ := s.read("x"); got != "select 1" {
		t.Fatalf("the draft file holds the text alone: %q", got)
	}
	wantOneOrphanUnder(t, s, now, "win-proj")
	r, text, err := s.Restore("x")
	if err != nil || text != "select 1" || r.Execution() != "win-proj" {
		t.Fatalf("%q %q %v", text, r.Execution(), err)
	}
	if err := r.SetExecution("other-proj"); err != nil {
		t.Fatal(err)
	}
	r.Release()
	wantOneOrphanUnder(t, s, now, "other-proj")
	r, _, _ = s.Restore("x")
	if err := r.SetExecution(""); err != nil || r.Execution() != "" {
		t.Fatalf("%v %q", err, r.Execution())
	}
	r.Release()
	wantOneOrphanUnder(t, s, now, "")
}

func TestADraftWithoutARecordHasAnUnknownExecutionProject(t *testing.T) {
	s := In(t.TempDir())
	if err := os.WriteFile(s.Path("old"), []byte("select 'old'"), 0o600); err != nil {
		t.Fatal(err)
	}
	wantOneOrphanUnder(t, s, time.Now(), "")
	d, _, err := s.Restore("old")
	if err != nil || d.Execution() != "" {
		t.Fatalf("%q %v", d.Execution(), err)
	}
	d.Release()
}

func TestTheExecutionRecordGoesWithTheLock(t *testing.T) {
	s := In(t.TempDir())
	now := time.Now()
	empty, _ := s.Hold("empty")
	_ = empty.SetExecution("p")
	empty.Release()
	discarded, _ := s.Hold("discarded")
	_ = discarded.SetExecution("p")
	_ = discarded.Save("select 1")
	discarded.Discard()
	stale, _ := s.Hold("stale")
	_ = stale.SetExecution("p")
	_ = stale.Save("select 1")
	stale.Release()
	at := now.Add(-maxAge - time.Hour)
	if err := os.Chtimes(s.Path("stale"), at, at); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.lockPath("lone"), []byte("p\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if o := s.Orphans(now, nil); len(o) != 0 {
		t.Fatalf("%+v", o)
	}
	entries, _ := os.ReadDir(s.dir)
	for _, e := range entries {
		t.Errorf("left behind: %s", e.Name())
	}
}

func TestSetExecutionRefusesWhatWouldNotReadBack(t *testing.T) {
	s := In(t.TempDir())
	d, err := s.Hold("x")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Release()
	if err := d.SetExecution("good-proj"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"two words", "two\nlines", "tab\there", "-leading-dash"} {
		if err := d.SetExecution(bad); !errors.Is(err, ErrNotAProjectID) {
			t.Fatalf("%q: want ErrNotAProjectID, got %v", bad, err)
		}
		if d.Execution() != "good-proj" || s.recordedExecution("x") != "good-proj" {
			t.Fatalf("%q: a refused value changes nothing: %q on disk %q", bad, d.Execution(), s.recordedExecution("x"))
		}
	}
}

func TestAPartialRecordReadsAsAnUnknownProject(t *testing.T) {
	s := In(t.TempDir())
	if err := os.WriteFile(s.Path("x"), []byte("select 1"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, partial := range []string{"", "proj", "project-b", "two\nlines"} {
		if err := os.WriteFile(s.lockPath("x"), []byte(partial), 0o600); err != nil {
			t.Fatal(err)
		}
		wantOneOrphanUnder(t, s, time.Now(), "")
		d, _, err := s.Restore("x")
		if err != nil {
			t.Fatal(err)
		}
		if d.Execution() != "" {
			t.Fatalf("%q without its line end is unknown, got %q", partial, d.Execution())
		}
		d.Release()
	}
}

func TestSetExecutionRewritesADamagedRecord(t *testing.T) {
	s := In(t.TempDir())
	d, err := s.Hold("x")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Release()
	if err := d.SetExecution("proj-a"); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(s.lockPath("x"), 0); err != nil {
		t.Fatal(err)
	}
	if err := d.SetExecution("proj-a"); err != nil {
		t.Fatal(err)
	}
	if got := s.recordedExecution("x"); got != "proj-a" {
		t.Fatalf("setting the same project again rewrites the record, got %q", got)
	}
}

func TestAFailedRecordReportsWhatIsOnDisk(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write a read-only file")
	}
	s := In(t.TempDir())
	d, err := s.Hold("x")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Release()
	if err := d.Save("select 1"); err != nil {
		t.Fatal(err)
	}
	if err := d.SetExecution("proj-a"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.lockPath("x"), []byte("proj-"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(s.lockPath("x"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := d.SetExecution("proj-b"); err == nil {
		t.Fatal("writing a read-only record should fail")
	}
	if d.Execution() != "" {
		t.Fatalf("after a failed write the draft reports the damaged record on disk as unknown, got %q", d.Execution())
	}
	if got, _ := s.read("x"); got != "select 1" {
		t.Fatalf("a failed record leaves the text alone: %q", got)
	}
	if err := os.Chmod(s.lockPath("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.SetExecution("proj-b"); err != nil || s.recordedExecution("x") != "proj-b" {
		t.Fatalf("a retry writes the record: %v, on disk %q", err, s.recordedExecution("x"))
	}
}

func TestRestoringAMissingDraftLeavesNoLock(t *testing.T) {
	s := In(t.TempDir())
	if _, _, err := s.Restore("gone"); !os.IsNotExist(err) {
		t.Fatalf("want a not-exist error, got %v", err)
	}
	if exists(t, s.lockPath("gone")) {
		t.Fatal("a failed restore leaves no lock file behind")
	}
	if _, err := s.Hold("gone"); err != nil {
		t.Fatalf("a failed restore releases the lock: %v", err)
	}
}

func TestRestoreTakesTheLockBeforeReadingTheText(t *testing.T) {
	s := In(t.TempDir())
	held, err := s.Hold("x")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	if _, _, err := s.Restore("x"); !errors.Is(err, ErrHeld) {
		t.Fatalf("a held draft is refused before its text is read: %v", err)
	}
}
