package archtest

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
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
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
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
			t.Errorf("%s: %d comment lines, budget %d. Code comments are banned: put the intent in names and tests, "+
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

func commentCounts(t *testing.T) map[string]int {
	t.Helper()
	out, err := exec.Command("git", "-C", "../..", "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", "*.go").Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	counts := map[string]int{}
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
		if n := commentLines(t, rel, src); n > 0 {
			counts[rel] = n
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

func commentLines(t *testing.T, name string, src []byte) int {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	n := 0
	for _, g := range f.Comments {
		for _, c := range g.List {
			if allowedComment(c.Text) {
				continue
			}
			n += fset.Position(c.End()).Line - fset.Position(c.Pos()).Line + 1
		}
	}
	return n
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
