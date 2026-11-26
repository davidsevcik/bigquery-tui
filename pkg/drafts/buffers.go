package drafts

type Buffer struct {
	Path     string
	Empty    bool
	Modified bool
	Ran      bool
}

func (b Buffer) Unsaved() bool { return b.Modified || (b.Path == "" && !b.Empty) }

func (b Buffer) Disposable() bool { return b.Path == "" && b.Empty && !b.Ran }
