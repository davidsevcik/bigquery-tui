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
		if _, err := Bind(api.ParamSpec{Name: name, Type: typ}, d); err != nil {
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

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '\'' || s[0] == '"') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
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
			d := *p.Default
			if d == "" || strings.TrimSpace(d) != d || strings.ContainsAny(d, `'"`) {
				d = `"` + d + `"`
			}
			b.WriteString(" = " + d)
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
	id, err := CleanID(id)
	if err != nil {
		return "", err
	}
	return filepath.Join(string(d), filepath.FromSlash(id)), nil
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
	f.ID = id
	f.Name = strings.TrimSuffix(path.Base(id), path.Ext(id))
	if st, err := os.Stat(p); err == nil {
		f.ModTime = st.ModTime()
	}
	return f, nil
}

// Path returns the file of a Favorite.
func (d Dir) Path(id string) (string, error) { return d.file(id) }

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
		// a link refuses an existing target, unlike rename
		if err := os.Link(tmp.Name(), p); err != nil {
			os.Remove(tmp.Name())
			if errors.Is(err, fs.ErrExist) {
				return "", fmt.Errorf("%w: %s", ErrExists, clean)
			}
			return "", err
		}
		os.Remove(tmp.Name())
		return clean, nil
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return clean, nil
}

// Rename moves a Favorite to a new name; the target must not exist.
func (d Dir) Rename(from, to string) (string, error) {
	src, err := d.file(from)
	if err != nil {
		return "", err
	}
	dst, err := d.file(to)
	if err != nil {
		return "", err
	}
	clean, _ := CleanID(to)
	if _, err := os.Stat(src); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("%w: %s", ErrNotFound, from)
		}
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	if err := os.Link(src, dst); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return "", fmt.Errorf("%w: %s", ErrExists, clean)
		}
		return "", err
	}
	if err := os.Remove(src); err != nil {
		os.Remove(dst)
		return "", err
	}
	d.pruneDirs(filepath.Dir(src))
	return clean, nil
}

// Delete removes a Favorite (there is no trash; git is the safety net).
func (d Dir) Delete(id string) error {
	p, err := d.file(id)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%w: %s", ErrNotFound, id)
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
