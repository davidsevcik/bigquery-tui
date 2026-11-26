package drafts

import "testing"

func TestUnsavedAndDisposable(t *testing.T) {
	for _, c := range []struct {
		name                string
		b                   Buffer
		unsaved, disposable bool
	}{
		{"an empty scratch buffer", Buffer{Empty: true}, false, true},
		{"a scratch buffer with text", Buffer{}, true, false},
		{"an empty scratch buffer a Run started from", Buffer{Empty: true, Ran: true}, false, false},
		{"a saved file", Buffer{Path: "/q/a.sql"}, false, false},
		{"a changed file", Buffer{Path: "/q/b.sql", Modified: true}, true, false},
		{"an empty file", Buffer{Path: "/q/c.sql", Empty: true}, false, false},
	} {
		if c.b.Unsaved() != c.unsaved || c.b.Disposable() != c.disposable {
			t.Errorf("%s: unsaved %v disposable %v", c.name, c.b.Unsaved(), c.b.Disposable())
		}
	}
}
