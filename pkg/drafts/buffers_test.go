package drafts

import "testing"

func TestUnsavedDisposableAndLostOnQuit(t *testing.T) {
	for _, c := range []struct {
		name                      string
		b                         Buffer
		unsaved, disposable, lost bool
	}{
		{"an empty scratch buffer", Buffer{Empty: true}, false, true, false},
		{"a scratch buffer with text in its draft", Buffer{Drafted: true}, true, false, false},
		{"a scratch buffer with text not in its draft", Buffer{}, true, false, true},
		{"an empty scratch buffer a Run started from", Buffer{Empty: true, Ran: true}, false, false, false},
		{"a saved file", Buffer{Path: "/q/a.sql"}, false, false, false},
		{"a changed file", Buffer{Path: "/q/b.sql", Modified: true}, true, false, true},
		{"an empty file", Buffer{Path: "/q/c.sql", Empty: true}, false, false, false},
	} {
		if c.b.Unsaved() != c.unsaved || c.b.Disposable() != c.disposable || c.b.LostOnQuit() != c.lost {
			t.Errorf("%s: unsaved %v disposable %v lost on quit %v", c.name, c.b.Unsaved(), c.b.Disposable(), c.b.LostOnQuit())
		}
	}
}
