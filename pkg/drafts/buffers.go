package drafts

// Buffer is what the drafts know of an open buffer, to tell when its text
// is at risk.
type Buffer struct {
	Path     string // a file buffer's file (a Favorite's too); "" for a scratch buffer
	Empty    bool   // its text is empty
	Modified bool   // a file buffer with changes not saved to its file
	Ran      bool   // a Run was started from it
}

// Unsaved reports whether closing or replacing the buffer would lose text: a
// file buffer's unsaved changes, or a scratch buffer's text, which lives only
// in its draft. A frontend asks before it does either.
func (b Buffer) Unsaved() bool { return b.Modified || (b.Path == "" && !b.Empty) }

// Disposable reports whether the buffer may be dropped without asking when
// a restored draft takes its place: an empty scratch buffer no Run was
// started from.
func (b Buffer) Disposable() bool { return b.Path == "" && b.Empty && !b.Ran }
