package drafts

type Buffer struct {
	Path     string
	Empty    bool
	Modified bool
	Ran      bool
	Drafted  bool
}

func (b Buffer) Unsaved() bool { return b.Modified || (b.Path == "" && !b.Empty) }

func (b Buffer) Disposable() bool { return b.Path == "" && b.Empty && !b.Ran }

func (b Buffer) LostOnQuit() bool { return b.Modified || (b.Path == "" && !b.Empty && !b.Drafted) }
