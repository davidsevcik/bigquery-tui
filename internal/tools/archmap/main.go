// Command archmap writes an interactive map of this module's architecture:
// the packages, the imports between them and each package's exported API.
// It reads the current source every time it runs.
//
//	go run ./internal/tools/archmap            # writes bin/architecture.html
//	go run ./internal/tools/archmap -open      # and opens it in the browser
//	go run ./internal/tools/archmap -o map.html
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

func main() {
	out := flag.String("o", "", "output file (default: bin/architecture.html in the module root)")
	open := flag.Bool("open", false, "open the page in the browser when done")
	flag.Parse()

	if err := run(*out, *open); err != nil {
		fmt.Fprintln(os.Stderr, "archmap:", err)
		os.Exit(1)
	}
}

func run(out string, open bool) error {
	m, warnings, err := Load(".")
	if err != nil {
		return err
	}
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, "archmap: warning:", w)
	}
	m.Generated = time.Now().Format("2006-01-02 15:04")

	if out == "" {
		out = filepath.Join(m.root, "bin", "architecture.html")
	}
	var buf bytes.Buffer
	if err := render(&buf, m); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(out, buf.Bytes(), 0o644); err != nil {
		return err
	}
	fmt.Println(out)

	if open {
		opener := "xdg-open"
		if runtime.GOOS == "darwin" {
			opener = "open"
		}
		return exec.Command(opener, out).Start()
	}
	return nil
}
