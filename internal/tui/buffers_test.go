package tui

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/davidsevcik/bigquery-tui/pkg/bq/bqtest"
	"github.com/davidsevcik/bigquery-tui/pkg/drafts"
)

type draftsAt struct {
	Backend
	dir string
}

func (b draftsAt) DraftDir() string { return b.dir }

type unholdable struct {
	*rig
	blocker string
	reason  string
	log     *bytes.Buffer
}

func unholdableDrafts(t *testing.T) *unholdable {
	t.Helper()
	base := newRig(t, 120, 30)
	blocker := filepath.Join(base.dir, "not-a-directory")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(blocker, "tui")
	reason := os.MkdirAll(dir, 0o700)
	if reason == nil {
		t.Fatal("a directory under a regular file cannot be made")
	}
	log := &bytes.Buffer{}
	m := New(draftsAt{base.m.be, dir}, Options{
		NoBackground: true, StableOutput: true, Location: time.UTC, JobHeld: base.fake.Held,
		Now: func() time.Time { return base.now }, Getenv: func(string) string { return "" },
		Log: slog.New(slog.NewTextHandler(log, nil)),
	})
	t.Cleanup(m.Close)
	r := &rig{t: t, m: m, fake: base.fake, eng: base.eng, dir: base.dir, cfg: base.cfg, now: base.now}
	r.send(teaSize(120, 30))
	return &unholdable{rig: r, blocker: blocker, reason: reason.Error(), log: log}
}

func (r *rig) failDraftWrites(b *buffer) (restore func()) {
	r.t.Helper()
	blocker := r.m.draftPath(b.id) + ".tmp"
	if err := os.MkdirAll(filepath.Join(blocker, "keep"), 0o700); err != nil {
		r.t.Fatal(err)
	}
	return func() {
		r.t.Helper()
		if err := os.RemoveAll(blocker); err != nil {
			r.t.Fatal(err)
		}
	}
}

func (u *unholdable) wantUnheld(b *buffer) {
	u.t.Helper()
	if b.draft != nil || b.draftErr != u.reason {
		u.t.Fatalf("the buffer has no draft and keeps the reason: %v %q", b.draft != nil, b.draftErr)
	}
	u.contains("draft not saved (retrying): ")
	if got := u.m.draftProblem(); got != u.reason {
		u.t.Fatalf("the status line gives the reason %q, got %q", u.reason, got)
	}
}

func TestEveryScratchBufferReportsADraftItCannotHold(t *testing.T) {
	for _, c := range []struct {
		name string
		make func(u *unholdable) *buffer
	}{
		{"startup", func(u *unholdable) *buffer {
			u.press("ctrl+w", "l")
			u.typeText("select 1")
			return u.m.buf()
		}},
		{"ctrl+x n", func(u *unholdable) *buffer {
			u.press("ctrl+w", "l", "ctrl+x", "n")
			u.typeText("select 1")
			return u.m.buf()
		}},
		{"a picker load", func(u *unholdable) *buffer {
			u.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.Numbers(1) }
			u.press("ctrl+w", "l")
			u.typeText("select 'run'")
			u.press("ctrl+r", "ctrl+x", "r", "enter")
			u.contains("Replace the text of query 1")
			u.press("n")
			if len(u.m.bufs) != 2 || u.text() != "select 'run'" {
				u.t.Fatalf("%d buffers, %q", len(u.m.bufs), u.text())
			}
			return u.m.buf()
		}},
		{"s SELECT", func(u *unholdable) *buffer {
			u.onSalesTable(0, "acme-prod.sales.customers")
			u.press("s")
			if len(u.m.bufs) != 2 {
				u.t.Fatalf("%d buffers", len(u.m.bufs))
			}
			return u.m.buf()
		}},
		{"the replacement for the last buffer closed", func(u *unholdable) *buffer {
			first := u.m.buf()
			u.press("ctrl+w", "l", "ctrl+x", "w")
			if len(u.m.bufs) != 1 || u.m.buf() == first {
				u.t.Fatal("closing the last buffer opens a new one")
			}
			u.typeText("select 1")
			return u.m.buf()
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			u := unholdableDrafts(t)
			u.wantUnheld(c.make(u))
		})
	}
}

func TestAnUnheldDraftIsHeldOnTheNextTick(t *testing.T) {
	u := unholdableDrafts(t)
	u.press("ctrl+w", "l")
	u.typeText("select 1")
	b := u.m.buf()
	u.wantUnheld(b)
	if err := os.Remove(u.blocker); err != nil {
		t.Fatal(err)
	}
	u.m.Update(tickMsg{})
	store := drafts.In(u.m.be.DraftDir())
	if got, err := os.ReadFile(store.Path(b.id)); err != nil || string(got) != "select 1" {
		t.Fatalf("the tick writes the text at once: %q %v", got, err)
	}
	if _, err := store.Hold(b.id); err == nil {
		t.Fatal("the buffer holds the draft's lock")
	}
	if b.draftErr != "" || u.m.draftProblem() != "" {
		t.Fatalf("the error clears: %q %q", b.draftErr, u.m.draftProblem())
	}
	u.lacks("draft not saved")
}

func TestAHoldFailureIsLoggedOncePerReason(t *testing.T) {
	u := unholdableDrafts(t)
	u.press("ctrl+w", "l")
	u.typeText("select 1")
	for range 3 {
		u.m.Update(tickMsg{})
	}
	logged := func() int { return strings.Count(u.log.String(), "level=WARN") }
	if n := logged(); n != 1 {
		t.Fatalf("one failing hold is logged once, got %d:\n%s", n, u.log)
	}
	if err := os.Remove(u.blocker); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(u.blocker, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(u.m.be.DraftDir(), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		u.m.Update(tickMsg{})
	}
	if n := logged(); n != 2 {
		t.Fatalf("a new reason is logged again, got %d:\n%s", n, u.log)
	}
	if u.m.buf().draftErr == u.reason || u.m.draftProblem() == "" {
		t.Fatalf("the status line has the new reason: %q", u.m.draftProblem())
	}
}

func TestADraftErrorClearsOnlyWhenEveryBufferSaves(t *testing.T) {
	r := editorRig(t, 120, 30)
	restoreA := r.failDraftWrites(r.m.buf())
	r.typeText("select 'a'")
	r.contains("draft not saved (retrying)")
	r.press("ctrl+x", "n")
	r.typeText("select 'b'")
	b := r.m.buf()
	if got, _ := os.ReadFile(r.m.draftPath(b.id)); string(got) != "select 'b'" {
		t.Fatalf("buffer b saves: %q", got)
	}
	r.contains("draft not saved (retrying)")
	restoreA()
	r.m.saveDrafts()
	r.lacks("draft not saved")
}

func TestQuitAsksWhenAScratchBufferIsNotInItsDraft(t *testing.T) {
	t.Run("an empty buffer without a draft", func(t *testing.T) {
		u := unholdableDrafts(t)
		u.press("ctrl+w", "l", "ctrl+q")
		if u.m.ov == ovConfirm {
			t.Fatal("an empty buffer has no text to lose")
		}
	})
	t.Run("text without a draft", func(t *testing.T) {
		u := unholdableDrafts(t)
		u.press("ctrl+w", "l")
		u.typeText("select 1")
		u.press("ctrl+q")
		if u.m.ov != ovConfirm {
			t.Fatal("text that is not in a draft is lost on quit")
		}
		u.contains("Unsaved changes will be lost in: query 1")
	})
	t.Run("text whose draft write fails", func(t *testing.T) {
		r := editorRig(t, 120, 30)
		r.failDraftWrites(r.m.buf())
		r.typeText("select 1")
		r.press("ctrl+q")
		if r.m.ov != ovConfirm {
			t.Fatal("text the draft does not hold is lost on quit")
		}
		r.contains("Unsaved changes will be lost in: query 1")
	})
	t.Run("text in its draft", func(t *testing.T) {
		r := editorRig(t, 120, 30)
		r.typeText("select 1")
		r.press("ctrl+q")
		if r.m.ov == ovConfirm {
			t.Fatal("a draft keeps the text for restore")
		}
	})
	t.Run("a buffer saved to a file", func(t *testing.T) {
		u := unholdableDrafts(t)
		u.press("ctrl+w", "l")
		u.typeText("select 1")
		u.press("ctrl+s")
		u.typeText(filepath.Join(u.dir, "saved.sql"))
		u.press("enter")
		if b := u.m.buf(); b.path == "" || b.draftErr != "" || u.m.draftProblem() != "" {
			t.Fatalf("a file buffer has no draft to fail: %q %q", b.path, b.draftErr)
		}
		u.lacks("draft not saved")
		u.press("ctrl+q")
		if u.m.ov == ovConfirm {
			t.Fatal("the text is in its file")
		}
	})
}

func TestATickAfterQuitLeavesTheReleasedDraftsAlone(t *testing.T) {
	r := editorRig(t, 120, 30)
	r.typeText("select 1")
	b := r.m.buf()
	r.m.quit()
	r.m.Update(tickMsg{})
	d, err := drafts.In(r.m.be.DraftDir()).Hold(b.id)
	if err != nil {
		t.Fatalf("quit released the draft for another instance: %v", err)
	}
	d.Release()
	if got, _ := os.ReadFile(r.m.draftPath(b.id)); string(got) != "select 1" {
		t.Fatalf("the draft keeps the text: %q", got)
	}
}

func TestAFreshBufferWhoseDraftIsHeldTakesAnotherID(t *testing.T) {
	r := editorRig(t, 120, 30)
	other, err := drafts.In(r.m.be.DraftDir()).Hold("taken")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Release()
	b := r.m.addBufferWith("", "", "taken", nil)
	if b.id == "taken" || b.draft == nil || b.draftErr != "" {
		t.Fatalf("id %q held %v err %q", b.id, b.draft != nil, b.draftErr)
	}
}
