package archtest

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

var updateCommentBudget = flag.Bool("update-comments", false, "lower the comment budget to today's counts")

const commentBudgetFile = "testdata/comment_budget.txt"

var allowedCommentPrefixes = []string{
	"//go:",
	"//nolint:",
	"//line ",
	"//export ",
	"// Code generated ",
	"// Output:",
	"// Unordered output:",
}

func TestCommentBudget(t *testing.T) {
	counts := commentCounts(t)
	budget := readCommentBudget(t)

	paths := map[string]bool{}
	for p := range counts {
		paths[p] = true
	}
	for p := range budget {
		paths[p] = true
	}
	sorted := make([]string, 0, len(paths))
	for p := range paths {
		sorted = append(sorted, p)
	}
	sort.Strings(sorted)

	lowered := map[string]int{}
	var stale []string
	total := 0
	for _, p := range sorted {
		have, allowed := counts[p], budget[p]
		total += have
		switch {
		case have > allowed:
			t.Errorf("%s: %d comment lines, budget %d. Code comments are banned beyond package docs and // ADR-NNNN pointers: put the intent in names and tests, "+
				"a lasting decision in docs/adr, and a known shortcut in an issue (see .claude/skills/delete-comment)",
				p, have, allowed)
			if allowed > 0 {
				lowered[p] = allowed
			}
		case have < allowed:
			stale = append(stale, fmt.Sprintf("%s: %d -> %d", p, allowed, have))
			if have > 0 {
				lowered[p] = have
			}
		default:
			if have > 0 {
				lowered[p] = have
			}
		}
	}
	t.Logf("%d comment lines left in %d files", total, len(counts))

	if len(stale) == 0 {
		return
	}
	if *updateCommentBudget {
		writeCommentBudget(t, lowered)
		return
	}
	t.Errorf("comments were deleted; lock the lower budget in with "+
		"`go test ./internal/archtest -run TestCommentBudget -update-comments`:\n%s", strings.Join(stale, "\n"))
}

type scannedFile struct {
	path     string
	pkg      string
	lines    int
	pkgDoc   bool
	pointers []adrPointer
}

type adrPointer struct {
	adr    string
	symbol string
	line   int
}

var adrPointerText = regexp.MustCompile(`^// ADR-(\d{4})$`)

func scanRepo(t *testing.T) []scannedFile {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	out, err := exec.Command("git", "-C", "../..", "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", "*.go").Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	var files []scannedFile
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel == "" || isTestdata(rel) {
			continue
		}
		src, err := os.ReadFile(filepath.Join("../..", rel))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, scanFile(t, rel, src))
	}
	return files
}

func commentCounts(t *testing.T) map[string]int {
	t.Helper()
	counts := map[string]int{}
	for _, f := range scanRepo(t) {
		if f.lines > 0 {
			counts[f.path] = f.lines
		}
	}
	return counts
}

func isTestdata(rel string) bool {
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == "testdata" {
			return true
		}
	}
	return false
}

func scanFile(t *testing.T, name string, src []byte) scannedFile {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	out := scannedFile{path: name, pkg: filepath.ToSlash(filepath.Dir(name)) + " " + f.Name.Name}
	for _, g := range f.Comments {
		if g == f.Doc {
			out.pkgDoc = true
			continue
		}
		for _, c := range g.List {
			if allowedComment(c.Text) {
				continue
			}
			if m := adrPointerText.FindStringSubmatch(c.Text); m != nil {
				out.pointers = append(out.pointers, adrPointer{adr: m[1], symbol: enclosingSymbol(f, c.Pos()), line: fset.Position(c.Pos()).Line})
				continue
			}
			out.lines += fset.Position(c.End()).Line - fset.Position(c.Pos()).Line + 1
		}
	}
	return out
}

func enclosingSymbol(f *ast.File, pos token.Pos) string {
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if within(pos, d.Doc, d.Pos(), d.End()) {
				return d.Name.Name
			}
		case *ast.GenDecl:
			if !within(pos, d.Doc, d.Pos(), d.End()) {
				continue
			}
			for _, spec := range d.Specs {
				if name := specName(spec, pos, len(d.Specs) == 1); name != "" {
					return name
				}
			}
		}
	}
	return ""
}

func specName(spec ast.Spec, pos token.Pos, only bool) string {
	switch s := spec.(type) {
	case *ast.TypeSpec:
		if only || within(pos, s.Doc, s.Pos(), s.End()) {
			return s.Name.Name
		}
	case *ast.ValueSpec:
		if only || within(pos, s.Doc, s.Pos(), s.End()) {
			return s.Names[0].Name
		}
	}
	return ""
}

func within(pos token.Pos, doc *ast.CommentGroup, from, to token.Pos) bool {
	if doc != nil {
		from = doc.Pos()
	}
	return pos >= from && pos <= to
}

func TestOnePackageDocPerPackage(t *testing.T) {
	docs := map[string][]string{}
	for _, f := range scanRepo(t) {
		if f.pkgDoc {
			docs[f.pkg] = append(docs[f.pkg], f.path)
		}
	}
	for pkg, files := range docs {
		if len(files) > 1 {
			sort.Strings(files)
			t.Errorf("package %s has a package doc in %d files, want one: %s", pkg, len(files), strings.Join(files, ", "))
		}
	}
}

func TestADRPointersNameTheirSymbol(t *testing.T) {
	adrs := map[string]string{}
	for _, f := range scanRepo(t) {
		for _, p := range f.pointers {
			where := fmt.Sprintf("%s:%d: ADR-%s", f.path, p.line, p.adr)
			if p.symbol == "" {
				t.Errorf("%s: a pointer must sit on or inside the declaration it is about", where)
				continue
			}
			text, ok := adrs[p.adr]
			if !ok {
				text = readADR(t, p.adr)
				adrs[p.adr] = text
			}
			if text == "" {
				t.Errorf("%s: no docs/adr/%s-*.md", where, p.adr)
				continue
			}
			if !regexp.MustCompile(`\b` + regexp.QuoteMeta(p.symbol) + `\b`).MatchString(text) {
				t.Errorf("%s: the ADR does not mention %s", where, p.symbol)
			}
		}
	}
}

func readADR(t *testing.T, number string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join("../../docs/adr", number+"-*.md"))
	if err != nil || len(matches) == 0 {
		return ""
	}
	b, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func allowedComment(text string) bool {
	for _, p := range allowedCommentPrefixes {
		if strings.HasPrefix(text, p) {
			return true
		}
	}
	return false
}

func readCommentBudget(t *testing.T) map[string]int {
	t.Helper()
	f, err := os.Open(commentBudgetFile)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]int{}
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	budget := map[string]int{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		count, path, ok := strings.Cut(line, " ")
		n, err := strconv.Atoi(count)
		if !ok || err != nil {
			t.Fatalf("%s: bad line %q, want \"<count> <path>\"", commentBudgetFile, line)
		}
		budget[path] = n
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return budget
}

func writeCommentBudget(t *testing.T, budget map[string]int) {
	t.Helper()
	paths := make([]string, 0, len(budget))
	for p := range budget {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var b strings.Builder
	for _, p := range paths {
		fmt.Fprintf(&b, "%d %s\n", budget[p], p)
	}
	if err := os.WriteFile(commentBudgetFile, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}
