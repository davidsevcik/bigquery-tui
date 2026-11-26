package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var directivePrefixes = []string{
	"//go:",
	"//nolint:",
	"//line ",
	"//export ",
	"// Code generated ",
	"// Output:",
	"// Unordered output:",
}

var adrPointer = regexp.MustCompile(`^// ADR-\d{4}$`)

type group struct {
	ID      string `json:"id"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	End     int    `json:"end"`
	OwnLine bool   `json:"own_line"`
	Text    string `json:"text"`
}

type verdict struct {
	ID  string `json:"id"`
	Cat string `json:"cat"`
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "list":
		list(os.Args[2:])
	case "strip":
		strip(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: commentkit list [-chunks N -out DIR] [path ...]\n       commentkit strip verdicts.jsonl ...")
	os.Exit(2)
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "commentkit:", err)
	os.Exit(1)
}

func isDirective(text string) bool {
	if adrPointer.MatchString(text) {
		return true
	}
	for _, p := range directivePrefixes {
		if strings.HasPrefix(text, p) {
			return true
		}
	}
	return false
}

func goFiles(paths []string) []string {
	args := append([]string{"ls-files", "--cached", "--others", "--exclude-standard", "--"}, paths...)
	if len(paths) == 0 {
		args = append(args, "*.go")
	}
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		die(fmt.Errorf("git ls-files: %w", err))
	}
	var files []string
	for _, f := range strings.Fields(string(out)) {
		if strings.HasSuffix(f, ".go") && !strings.Contains("/"+f, "/testdata/") {
			if _, err := os.Stat(f); err == nil {
				files = append(files, f)
			}
		}
	}
	return files
}

func groups(file string) []group {
	src, err := os.ReadFile(file)
	if err != nil {
		die(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		die(err)
	}
	lines := strings.Split(string(src), "\n")
	var out []group
	for _, g := range f.Comments {
		if g == f.Doc {
			continue
		}
		var text []string
		for _, c := range g.List {
			if !isDirective(c.Text) {
				text = append(text, c.Text)
			}
		}
		if len(text) == 0 {
			continue
		}
		start, end := fset.Position(g.Pos()), fset.Position(g.End())
		out = append(out, group{
			ID:      file + ":" + strconv.Itoa(start.Line),
			File:    file,
			Line:    start.Line,
			End:     end.Line,
			OwnLine: strings.TrimSpace(lines[start.Line-1][:start.Column-1]) == "",
			Text:    strings.Join(text, "\n"),
		})
	}
	return out
}

func list(args []string) {
	fl := flag.NewFlagSet("list", flag.ExitOnError)
	chunks := fl.Int("chunks", 0, "split the ledger into N files balanced by comment size, keeping each file whole")
	outDir := fl.String("out", "", "directory for the chunk files")
	_ = fl.Parse(args)
	var byFile [][]group
	total := 0
	for _, f := range goFiles(fl.Args()) {
		if gs := groups(f); len(gs) > 0 {
			byFile = append(byFile, gs)
			for _, g := range gs {
				total += len(g.Text)
			}
		}
	}
	if *chunks <= 1 {
		w := bufio.NewWriter(os.Stdout)
		for _, gs := range byFile {
			writeGroups(w, gs)
		}
		if err := w.Flush(); err != nil {
			die(err)
		}
		return
	}
	if *outDir == "" {
		die(fmt.Errorf("-chunks needs -out"))
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		die(err)
	}
	target := total / *chunks
	n, size := 0, 0
	var cur []group
	flush := func() {
		if len(cur) == 0 {
			return
		}
		path := filepath.Join(*outDir, fmt.Sprintf("chunk%d.jsonl", n))
		f, err := os.Create(path)
		if err != nil {
			die(err)
		}
		w := bufio.NewWriter(f)
		writeGroups(w, cur)
		if err := w.Flush(); err != nil {
			die(err)
		}
		f.Close()
		fmt.Printf("%s: %d comments\n", path, len(cur))
		n, size, cur = n+1, 0, nil
	}
	for _, gs := range byFile {
		if size >= target && n < *chunks-1 {
			flush()
		}
		cur = append(cur, gs...)
		for _, g := range gs {
			size += len(g.Text)
		}
	}
	flush()
}

func writeGroups(w *bufio.Writer, gs []group) {
	enc := json.NewEncoder(w)
	for _, g := range gs {
		if err := enc.Encode(g); err != nil {
			die(err)
		}
	}
}

func strip(paths []string) {
	if len(paths) == 0 {
		usage()
	}
	byFile := map[string]map[int]bool{}
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			die(err)
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			if strings.TrimSpace(sc.Text()) == "" {
				continue
			}
			var v verdict
			if err := json.Unmarshal(sc.Bytes(), &v); err != nil {
				die(fmt.Errorf("%s: %w", p, err))
			}
			if v.Cat == "keep" {
				continue
			}
			i := strings.LastIndexByte(v.ID, ':')
			line, err := strconv.Atoi(v.ID[i+1:])
			if i < 0 || err != nil {
				die(fmt.Errorf("%s: id %q is not file:line", p, v.ID))
			}
			if byFile[v.ID[:i]] == nil {
				byFile[v.ID[:i]] = map[int]bool{}
			}
			byFile[v.ID[:i]][line] = true
		}
		if err := sc.Err(); err != nil {
			die(err)
		}
		f.Close()
	}
	files := make([]string, 0, len(byFile))
	for f := range byFile {
		files = append(files, f)
	}
	sort.Strings(files)
	removed := 0
	for _, f := range files {
		removed += stripFile(f, byFile[f])
	}
	fmt.Printf("removed %d comment groups from %d files\n", removed, len(files))
}

type span struct{ from, to int }

func stripFile(file string, lines map[int]bool) int {
	src, err := os.ReadFile(file)
	if err != nil {
		die(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, parser.ParseComments)
	if err != nil {
		die(err)
	}
	var spans []span
	found := 0
	for _, g := range f.Comments {
		if !lines[fset.Position(g.Pos()).Line] {
			continue
		}
		found++
		for _, c := range g.List {
			if !isDirective(c.Text) {
				spans = append(spans, commentSpan(src, fset.Position(c.Pos()).Offset, fset.Position(c.End()).Offset))
			}
		}
	}
	if found != len(lines) {
		die(fmt.Errorf("%s: %d comment groups asked for, %d found; the ledger is out of date", file, len(lines), found))
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].from > spans[j].from })
	out := append([]byte(nil), src...)
	for _, s := range spans {
		out = append(out[:s.from:s.from], out[s.to:]...)
	}
	res, err := format.Source(out)
	if err != nil {
		die(fmt.Errorf("%s: %w", file, err))
	}
	if err := os.WriteFile(file, res, 0o644); err != nil {
		die(err)
	}
	return found
}

func commentSpan(src []byte, from, to int) span {
	lineStart := strings.LastIndexByte(string(src[:from]), '\n') + 1
	after := to
	for after < len(src) && (src[after] == ' ' || src[after] == '\t') {
		after++
	}
	if strings.TrimSpace(string(src[lineStart:from])) == "" && (after == len(src) || src[after] == '\n') {
		if after < len(src) {
			after++
		}
		return span{lineStart, after}
	}
	for from > lineStart && (src[from-1] == ' ' || src[from-1] == '\t') {
		from--
	}
	return span{from, to}
}
