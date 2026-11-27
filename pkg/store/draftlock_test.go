package store_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/davidsevcik/bigquery-tui/pkg/drafts"
	"github.com/davidsevcik/bigquery-tui/pkg/store"
)

func TestAHoldThatRacesTheRemovalOfADraftLockLeavesOneHolder(t *testing.T) {
	for _, c := range []struct {
		name   string
		holder func(t *testing.T, s *drafts.Store) (remove func())
	}{
		{"lone-lock cleanup", func(t *testing.T, s *drafts.Store) func() {
			if err := os.WriteFile(filepath.Join(filepath.Dir(s.Path("x")), "x.lock"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			return func() { s.Orphans(time.Now(), nil) }
		}},
		{"release of an empty draft", func(t *testing.T, s *drafts.Store) func() {
			a, err := s.Hold("x")
			if err != nil {
				t.Fatal(err)
			}
			return a.Release
		}},
		{"discard", func(t *testing.T, s *drafts.Store) func() {
			a, err := s.Hold("x")
			if err != nil {
				t.Fatal(err)
			}
			if err := a.Save("select 'a'"); err != nil {
				t.Fatal(err)
			}
			return a.Discard
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := drafts.In(t.TempDir())
			remove := c.holder(t, s)
			store.OnceBetweenOpenAndFlock(t, func(string) { remove() })
			b, err := s.Hold("x")
			if err != nil {
				t.Fatal(err)
			}
			defer b.Release()
			if err := b.Save("select 'b'"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Hold("x"); !errors.Is(err, drafts.ErrHeld) {
				t.Fatalf("a draft held by B must not be held again, got %v", err)
			}
			if o := s.Orphans(time.Now(), nil); len(o) != 0 {
				t.Fatalf("a held draft is no orphan: %+v", o)
			}
		})
	}
}
