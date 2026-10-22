package main

import (
	"bytes"
	"go/ast"
	"go/doc"
	"go/printer"
	"go/token"
	"strings"
)

// extractAPI returns the exported declarations of one package and the first
// sentence of its package comment. files must be parsed with fset and with
// comments.
func extractAPI(fset *token.FileSet, files []*ast.File, importPath string) (API, string, error) {
	pkg, err := doc.NewFromFiles(fset, files, importPath)
	if err != nil {
		return API{}, "", err
	}
	api := emptyAPI()
	for _, v := range pkg.Consts {
		api.Consts = append(api.Consts, valueSymbol(fset, pkg, v, "const"))
	}
	for _, v := range pkg.Vars {
		api.Vars = append(api.Vars, valueSymbol(fset, pkg, v, "var"))
	}
	for _, f := range pkg.Funcs {
		api.Funcs = append(api.Funcs, funcSymbol(fset, pkg, f, "func"))
	}
	for _, t := range pkg.Types {
		td := TypeDoc{Symbol: typeSymbol(fset, pkg, t), Members: []Symbol{}}
		for _, v := range t.Consts {
			td.Members = append(td.Members, valueSymbol(fset, pkg, v, "const"))
		}
		for _, v := range t.Vars {
			td.Members = append(td.Members, valueSymbol(fset, pkg, v, "var"))
		}
		for _, f := range t.Funcs {
			td.Members = append(td.Members, funcSymbol(fset, pkg, f, "func"))
		}
		for _, f := range t.Methods {
			td.Members = append(td.Members, funcSymbol(fset, pkg, f, "method"))
		}
		api.Types = append(api.Types, td)
	}
	return api, pkg.Synopsis(pkg.Doc), nil
}

// emptyAPI has non-nil lists, so they serialize as [] rather than null.
func emptyAPI() API {
	return API{Types: []TypeDoc{}, Funcs: []Symbol{}, Consts: []Symbol{}, Vars: []Symbol{}}
}

func funcSymbol(fset *token.FileSet, pkg *doc.Package, f *doc.Func, kind string) Symbol {
	decl := *f.Decl
	decl.Body, decl.Doc = nil, nil
	return Symbol{Name: f.Name, Kind: kind, Sig: printNode(fset, &decl), Doc: pkg.Synopsis(f.Doc)}
}

func valueSymbol(fset *token.FileSet, pkg *doc.Package, v *doc.Value, kind string) Symbol {
	decl := *v.Decl
	decl.Doc = nil
	return Symbol{Name: strings.Join(v.Names, ", "), Kind: kind, Sig: printNode(fset, &decl), Doc: pkg.Synopsis(v.Doc)}
}

func typeSymbol(fset *token.FileSet, pkg *doc.Package, t *doc.Type) Symbol {
	decl := *t.Decl
	decl.Doc = nil
	kind := "type"
	if len(decl.Specs) == 1 {
		if spec, ok := decl.Specs[0].(*ast.TypeSpec); ok {
			switch {
			case spec.Assign.IsValid():
				kind = "alias"
			default:
				switch spec.Type.(type) {
				case *ast.StructType:
					kind = "struct"
				case *ast.InterfaceType:
					kind = "interface"
				case *ast.FuncType:
					kind = "func type"
				}
			}
		}
	}
	return Symbol{Name: t.Name, Kind: kind, Sig: printNode(fset, &decl), Doc: pkg.Synopsis(t.Doc)}
}

// printNode prints a declaration without comments, indented with spaces.
// go/printer notes fields that go/doc filtered out.
func printNode(fset *token.FileSet, node ast.Node) string {
	var buf bytes.Buffer
	cfg := printer.Config{Mode: printer.UseSpaces, Tabwidth: 4}
	if err := cfg.Fprint(&buf, fset, node); err != nil {
		return ""
	}
	return buf.String()
}
