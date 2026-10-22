package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// frontendPackages are the frontends from spec section 3, besides cmd/*.
var frontendPackages = map[string]bool{
	"internal/cli": true,
	"internal/tui": true,
	"internal/mcp": true,
}

// excludedPrefix hides developer tools (this generator) from the map.
const excludedPrefix = "internal/tools/"

type listPackage struct {
	ImportPath   string
	Name         string
	Dir          string
	GoFiles      []string
	TestGoFiles  []string
	XTestGoFiles []string
	Imports      []string
	Standard     bool
	Module       *struct{ Path string }
}

// Load reads the module that contains dir and builds the full model:
// packages, imports, public API and graph layout. Problems that do not stop
// the map (a file that does not parse) come back as warnings.
func Load(dir string) (*Model, []string, error) {
	modPath, root, err := moduleOf(dir)
	if err != nil {
		return nil, nil, err
	}
	out, err := goCmd(root, "list", "-e", "-deps",
		"-json=ImportPath,Name,Dir,GoFiles,TestGoFiles,XTestGoFiles,Imports,Standard,Module", "./...")
	if err != nil {
		return nil, nil, err
	}
	all := map[string]*listPackage{}
	var order []*listPackage
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var p listPackage
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			return nil, nil, fmt.Errorf("decode go list output: %w", err)
		}
		all[p.ImportPath] = &p
		order = append(order, &p)
	}

	m := &Model{Module: modPath, root: root}
	short := func(path string) (string, bool) {
		if path == modPath {
			return filepath.Base(path), true
		}
		s, ok := strings.CutPrefix(path, modPath+"/")
		if !ok || strings.HasPrefix(s+"/", excludedPrefix) {
			return "", false
		}
		return s, true
	}

	byShort := map[string]*Package{}
	ext := map[string]*External{}
	var warnings []string
	for _, lp := range order {
		if lp.Module == nil || lp.Module.Path != modPath {
			continue
		}
		s, ok := short(lp.ImportPath)
		if !ok {
			continue
		}
		p := &Package{
			Path:       lp.ImportPath,
			Short:      s,
			Label:      strings.TrimPrefix(s, "internal/"),
			Name:       lp.Name,
			Group:      classify(s, lp),
			Files:      len(lp.GoFiles),
			Imports:    []string{},
			ImportedBy: []string{},
			ThirdParty: []string{},
			API:        emptyAPI(),
			dir:        lp.Dir,
			goFiles:    lp.GoFiles,
		}
		thirdParty := map[string]bool{}
		for _, imp := range lp.Imports {
			dep := all[imp]
			if dep == nil || dep.Standard || dep.Module == nil {
				continue
			}
			if dep.Module.Path == modPath {
				if ds, ok := short(imp); ok {
					p.Imports = append(p.Imports, ds)
				}
				continue
			}
			thirdParty[dep.Module.Path] = true
		}
		for mod := range thirdParty {
			p.ThirdParty = append(p.ThirdParty, mod)
			e := ext[mod]
			if e == nil {
				e = &External{Module: mod}
				ext[mod] = e
			}
			e.Importers = append(e.Importers, s)
		}
		sort.Strings(p.Imports)
		sort.Strings(p.ThirdParty)

		fset := token.NewFileSet()
		files, errs := parseFiles(fset, p.dir, p.goFiles)
		warnings = append(warnings, errs...)
		if len(files) > 0 {
			api, synopsis, err := extractAPI(fset, files, p.Path)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s: %v", s, err))
			} else {
				p.API, p.Doc = api, synopsis
			}
		}
		p.Exported = p.API.count()
		byShort[s] = p
		m.Packages = append(m.Packages, p)
	}

	for _, p := range m.Packages {
		for _, imp := range p.Imports {
			m.Edges = append(m.Edges, Edge{From: p.Short, To: imp})
			if q := byShort[imp]; q != nil {
				q.ImportedBy = append(q.ImportedBy, p.Short)
			}
		}
	}
	for _, p := range m.Packages {
		sort.Strings(p.ImportedBy)
	}
	sort.Slice(m.Packages, func(i, j int) bool { return m.Packages[i].Short < m.Packages[j].Short })
	sort.Slice(m.Edges, func(i, j int) bool {
		if m.Edges[i].From != m.Edges[j].From {
			return m.Edges[i].From < m.Edges[j].From
		}
		return m.Edges[i].To < m.Edges[j].To
	})
	for _, e := range ext {
		sort.Strings(e.Importers)
		m.External = append(m.External, e)
	}
	sort.Slice(m.External, func(i, j int) bool { return m.External[i].Module < m.External[j].Module })

	arrange(m)
	return m, warnings, nil
}

// classify puts a package in one of the groups of spec section 3.
func classify(short string, lp *listPackage) string {
	switch {
	case len(lp.GoFiles) == 0 && len(lp.TestGoFiles)+len(lp.XTestGoFiles) > 0:
		return "test"
	case strings.HasPrefix(short, "cmd/") || frontendPackages[short]:
		return "frontend"
	default:
		return "core"
	}
}

func parseFiles(fset *token.FileSet, dir string, names []string) ([]*ast.File, []string) {
	var files []*ast.File
	var warnings []string
	for _, name := range names {
		path := filepath.Join(dir, name)
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("parse %s: %v", path, err))
		}
		if f != nil {
			files = append(files, f)
		}
	}
	return files, warnings
}

func moduleOf(dir string) (path, root string, err error) {
	out, err := goCmd(dir, "list", "-m", "-json")
	if err != nil {
		return "", "", err
	}
	var mod struct{ Path, Dir string }
	if err := json.Unmarshal(out, &mod); err != nil {
		return "", "", fmt.Errorf("decode go list -m output: %w", err)
	}
	if mod.Path == "" || mod.Dir == "" {
		return "", "", fmt.Errorf("no Go module found from %s", dir)
	}
	return mod.Path, mod.Dir, nil
}

func goCmd(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return out, nil
}
