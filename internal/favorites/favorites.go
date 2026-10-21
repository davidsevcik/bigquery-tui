// Package favorites reads and writes Favorites: plain .sql files under
// favorites_dir whose leading "-- key: value" comment lines carry a
// description, tags and typed parameters (spec section 11). The header is
// SQL comments, so a Favorite still runs anywhere as it is. There is no
// index: a directory walk is fast enough below a few thousand files.
package favorites

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

// Types are the parameter types a header may declare.
var Types = []string{"STRING", "INT64", "FLOAT64", "BOOL", "DATE", "DATETIME", "TIMESTAMP"}

// Errors.
var (
	ErrExists   = errors.New("a favorite with that name already exists")
	ErrNotFound = errors.New("no such favorite")
	ErrBadID    = errors.New("invalid favorite name")
	// ErrMoved refuses a rename or delete of a Favorite whose name now
	// leads to another file (favorites_dir changed since it was read).
	ErrMoved = errors.New("favorites_dir changed since the favorite was shown")
)

func validType(t string) bool {
	for _, x := range Types {
		if x == t {
			return true
		}
	}
	return false
}

// Parse splits a Favorite file into its header fields and the SQL body. The
// header is the run of leading lines of the form "-- description: …",
// "-- tags: …" or "-- param: name TYPE [= default]"; the first other line
// starts the body. A header line that cannot be read is reported in
// Problems and kept out of the fields.
func Parse(text string) api.Favorite {
	f := api.Favorite{Text: text}
	lines := strings.SplitAfter(text, "\n")
	i := 0
	for ; i < len(lines); i++ {
		key, val, ok := headerLine(lines[i])
		if !ok {
			break
		}
		switch key {
		case "description":
			if f.Description != "" {
				f.Description += " "
			}
			f.Description += val
		case "tags":
			for _, t := range strings.Split(val, ",") {
				if t = strings.TrimSpace(t); t != "" && !contains(f.Tags, t) {
					f.Tags = append(f.Tags, t)
				}
			}
		case "param":
			p, err := parseParam(val)
			if err != nil {
				f.Problems = append(f.Problems, fmt.Sprintf("line %d: %v", i+1, err))
				continue
			}
			dup := false
			for _, q := range f.Params {
				dup = dup || q.Name == p.Name
			}
			if dup {
				f.Problems = append(f.Problems, fmt.Sprintf("line %d: parameter @%s is declared twice", i+1, p.Name))
				continue
			}
			f.Params = append(f.Params, p)
		}
	}
	f.SQL = strings.Join(lines[i:], "")
	return f
}

func headerLine(l string) (key, val string, ok bool) {
	s := strings.TrimSpace(l)
	rest, isComment := strings.CutPrefix(s, "--")
	if !isComment {
		return "", "", false
	}
	k, v, found := strings.Cut(rest, ":")
	if !found {
		return "", "", false
	}
	k = strings.ToLower(strings.TrimSpace(k))
	switch k {
	case "description", "tags", "param":
		return k, strings.TrimSpace(v), true
	}
	return "", "", false
}

func parseParam(s string) (api.ParamSpec, error) {
	decl, def, hasDef := strings.Cut(s, "=")
	fields := strings.Fields(decl)
	if len(fields) != 2 {
		return api.ParamSpec{}, fmt.Errorf("want \"param: name TYPE [= default]\", got %q", s)
	}
	name := strings.TrimPrefix(fields[0], "@")
	if !validName(name) {
		return api.ParamSpec{}, fmt.Errorf("%q is not a parameter name", fields[0])
	}
	typ := strings.ToUpper(fields[1])
	if !validType(typ) {
		return api.ParamSpec{}, fmt.Errorf("@%s: unknown type %s (one of %s)", name, fields[1], strings.Join(Types, ", "))
	}
	p := api.ParamSpec{Name: name, Type: typ}
	if hasDef {
		d := unquote(strings.TrimSpace(def))
		if _, err := bindValue(api.ParamSpec{Name: name, Type: typ}, d); err != nil {
			return api.ParamSpec{}, fmt.Errorf("@%s: default: %v", name, err)
		}
		p.Default = &d
	}
	return p, nil
}

func validName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if r != '_' && !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// unquote reads a default: "double quoted" with Go escapes (\n, \", \\),
// 'single quoted' as is, or bare.
func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		if u, err := strconv.Unquote(s); err == nil {
			return u
		}
	}
	if len(s) >= 2 && (s[0] == '\'' || s[0] == '"') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

// quoteDefault writes a default so unquote reads it back: bare when it is
// plain, else double quoted with escapes (a line break must never end the
// comment line).
func quoteDefault(d string) string {
	plain := d != "" && strings.TrimSpace(d) == d && d[0] != '\'' && d[0] != '"'
	for _, r := range d {
		if r < ' ' || r == 0x7f || r == '\\' || r == 0x2028 || r == 0x2029 {
			plain = false
		}
	}
	if plain {
		return d
	}
	return strconv.Quote(d)
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// Render writes the header for the fields and appends the SQL body.
func Render(f api.Favorite) string {
	var b strings.Builder
	if d := strings.Join(strings.Fields(f.Description), " "); d != "" {
		b.WriteString("-- description: " + d + "\n")
	}
	if len(f.Tags) > 0 {
		b.WriteString("-- tags: " + strings.Join(f.Tags, ", ") + "\n")
	}
	for _, p := range f.Params {
		if p.Type == "" {
			continue // undeclared: nothing to write
		}
		b.WriteString("-- param: " + p.Name + " " + p.Type)
		if p.Default != nil {
			b.WriteString(" = " + quoteDefault(*p.Default))
		}
		b.WriteString("\n")
	}
	body := f.SQL
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	b.WriteString(body)
	return b.String()
}

// CleanID normalises a Favorite name into its ID: a relative path with
// forward slashes ending in .sql. It refuses absolute paths, "..", empty and
// hidden segments.
func CleanID(name string) (string, error) {
	s := strings.TrimSpace(strings.ReplaceAll(name, `\`, "/"))
	if s == "" || strings.HasPrefix(s, "/") {
		return "", fmt.Errorf("%w: %q", ErrBadID, name)
	}
	if !strings.HasSuffix(strings.ToLower(s), ".sql") {
		s += ".sql"
	}
	for _, seg := range strings.Split(s, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, ".") {
			return "", fmt.Errorf("%w: %q", ErrBadID, name)
		}
	}
	return path.Clean(s), nil
}

// Dir is a favorites directory.
type Dir string

func (d Dir) file(id string) (string, error) {
	clean, err := CleanID(id)
	if err != nil {
		return "", err
	}
	p := filepath.Join(string(d), filepath.FromSlash(clean))
	if err := d.contained(p); err != nil {
		return "", err
	}
	return p, nil
}

// contained refuses a path that a symlink (a linked folder, or the file
// itself) leads out of the favorites directory. The deepest part of the path
// that exists is resolved and must stay under the resolved root.
func (d Dir) contained(p string) error {
	root, err := filepath.EvalSymlinks(string(d))
	if errors.Is(err, fs.ErrNotExist) {
		return nil // nothing exists yet, so nothing can link out
	}
	if err != nil {
		return err
	}
	at := p
	for {
		if _, err := os.Lstat(at); err == nil {
			break
		}
		parent := filepath.Dir(at)
		if parent == at {
			return nil
		}
		at = parent
	}
	real, err := filepath.EvalSymlinks(at)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %s is a broken link", ErrBadID, at)
	}
	if err != nil {
		return err
	}
	if real != root && !strings.HasPrefix(real, root+string(filepath.Separator)) {
		return fmt.Errorf("%w: %s leads out of the favorites directory through a link", ErrBadID, at)
	}
	return nil
}

// List reads every Favorite, sorted by ID. A missing directory is empty.
// Hidden directories (.git) are skipped.
func (d Dir) List() ([]api.Favorite, error) {
	var out []api.Favorite
	root := string(d)
	err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			if p == root && errors.Is(err, fs.ErrNotExist) {
				return fs.SkipAll
			}
			return err
		}
		name := e.Name()
		if e.IsDir() {
			if p != root && strings.HasPrefix(name, ".") {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") || !strings.EqualFold(filepath.Ext(name), ".sql") || !e.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		f, err := d.read(filepath.ToSlash(rel), p)
		if err != nil {
			return err
		}
		out = append(out, f)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (d Dir) read(id, p string) (api.Favorite, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return api.Favorite{}, err
	}
	f := Parse(string(data))
	f.ID, f.Path = id, p
	f.Name = strings.TrimSuffix(path.Base(id), path.Ext(id))
	if st, err := os.Stat(p); err == nil {
		f.ModTime = st.ModTime()
	}
	return f, nil
}

// IDOf returns the Favorite ID of a file inside the directory, or "" for a
// file elsewhere (or not a .sql file). Links are resolved on both sides.
func (d Dir) IDOf(file string) string {
	if d == "" || file == "" {
		return ""
	}
	real := func(p string) string {
		p = filepath.Clean(p)
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return p
	}
	rel, err := filepath.Rel(real(string(d)), real(file))
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return ""
	}
	id, err := CleanID(filepath.ToSlash(rel))
	if err != nil || !strings.EqualFold(filepath.Ext(rel), ".sql") {
		return ""
	}
	return id
}

// shown returns the file of a Favorite as it was read (by List or Read),
// refusing one whose ID now leads to another file: a reload of
// favorites_dir in the meantime must not redirect a rename or a delete.
func (d Dir) shown(f api.Favorite) (string, error) {
	p, err := d.file(f.ID)
	if err != nil {
		return "", err
	}
	if f.Path == "" || p != f.Path {
		return "", fmt.Errorf("%w: %s", ErrMoved, f.ID)
	}
	return p, nil
}

// Read reads one Favorite.
func (d Dir) Read(id string) (api.Favorite, error) {
	p, err := d.file(id)
	if err != nil {
		return api.Favorite{}, err
	}
	clean, _ := CleanID(id)
	f, err := d.read(clean, p)
	if errors.Is(err, fs.ErrNotExist) {
		return f, fmt.Errorf("%w: %s", ErrNotFound, clean)
	}
	return f, err
}

// Write saves the text as a Favorite and returns its ID. Without overwrite
// an existing file is an ErrExists. The file is replaced atomically.
func (d Dir) Write(id, text string, overwrite bool) (string, error) {
	p, err := d.file(id)
	if err != nil {
		return "", err
	}
	clean, _ := CleanID(id)
	if !overwrite {
		if _, err := os.Lstat(p); err == nil {
			return "", fmt.Errorf("%w: %s", ErrExists, clean)
		}
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".bqt-*.tmp")
	if err != nil {
		return "", err
	}
	_, werr := tmp.WriteString(text)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	if !overwrite {
		// claim the name first (O_EXCL refuses an existing file); the rename
		// below then replaces only our own empty placeholder
		if err := claim(p); err != nil {
			os.Remove(tmp.Name())
			if errors.Is(err, fs.ErrExist) {
				return "", fmt.Errorf("%w: %s", ErrExists, clean)
			}
			return "", err
		}
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return clean, nil
}

// Rename moves a Favorite, as it was read, to a new name and returns it
// there; the target must not exist. A Favorite whose name now leads to
// another file is an ErrMoved.
func (d Dir) Rename(f api.Favorite, to string) (api.Favorite, error) {
	src, err := d.shown(f)
	if err != nil {
		return api.Favorite{}, err
	}
	dst, err := d.file(to)
	if err != nil {
		return api.Favorite{}, err
	}
	clean, _ := CleanID(to)
	if _, err := os.Stat(src); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return api.Favorite{}, fmt.Errorf("%w: %s", ErrNotFound, f.ID)
		}
		return api.Favorite{}, err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return api.Favorite{}, err
	}
	if err := claim(dst); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return api.Favorite{}, fmt.Errorf("%w: %s", ErrExists, clean)
		}
		return api.Favorite{}, err
	}
	if err := os.Rename(src, dst); err != nil {
		os.Remove(dst)
		return api.Favorite{}, err
	}
	d.pruneDirs(filepath.Dir(src))
	return d.read(clean, dst)
}

// claim creates an empty file that must not exist yet.
func claim(p string) error {
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	return f.Close()
}

// Delete removes a Favorite, as it was read (there is no trash; git is the
// safety net). A Favorite whose name now leads to another file is an
// ErrMoved.
func (d Dir) Delete(f api.Favorite) error {
	p, err := d.shown(f)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%w: %s", ErrNotFound, f.ID)
		}
		return err
	}
	d.pruneDirs(filepath.Dir(p))
	return nil
}

// pruneDirs removes now-empty subfolders up to the root.
func (d Dir) pruneDirs(dir string) {
	root := filepath.Clean(string(d))
	for dir = filepath.Clean(dir); dir != root && strings.HasPrefix(dir, root+string(filepath.Separator)); dir = filepath.Dir(dir) {
		if os.Remove(dir) != nil {
			return
		}
	}
}
