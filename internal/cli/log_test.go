package cli

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/davidsevcik/bigquery-tui/pkg/api"
	"github.com/davidsevcik/bigquery-tui/pkg/bq/bqtest"
	"github.com/davidsevcik/bigquery-tui/pkg/logging"
	"github.com/davidsevcik/bigquery-tui/pkg/store"
)

func failLogClose(s *sandbox) string {
	path := filepath.Join(store.PathsFor(filepath.Join(s.dir, "data")).LogDir, logging.FileName)
	s.logErr = &fs.PathError{Op: "close", Path: path, Err: syscall.EIO}
	return "warning: closing the log file " + path + ": " + syscall.EIO.Error()
}

func oneDocument(t *testing.T, out string) {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(out))
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("stdout holds no JSON document: %v\n%s", err, out)
	}
	if err := dec.Decode(&v); !errors.Is(err, io.EOF) {
		t.Fatalf("stdout holds more than one JSON document:\n%s", out)
	}
}

func stderrLines(s string) []string {
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

func TestFailedLogCloseWarnsAfterSyncAndKeepsTheExitCode(t *testing.T) {
	s := newSandbox(t)
	s.writeConfig("projects = [\"proj-a\"]\n")
	warning := failLogClose(s)

	code, out, errOut := s.run("sync")
	if code != api.ExitOK {
		t.Fatalf("exit %d %s", code, errOut)
	}
	oneDocument(t, out)
	if errOut != warning+"\n" {
		t.Fatalf("want exactly the warning on stderr, got %q", errOut)
	}

	s.fake.Projects["proj-a"].ListErr = bqtest.PermissionError("datasets.list")
	code, out, errOut = s.run("sync")
	if code != api.ExitBigQuery {
		t.Fatalf("a failed unit still exits %d, got %d", api.ExitBigQuery, code)
	}
	oneDocument(t, out)
	if strings.Count(errOut, "warning: closing the log file") != 1 || !strings.Contains(errOut, warning+"\n") {
		t.Fatalf("want the warning once, got %q", errOut)
	}
}

func TestFailedLogCloseWarnsAfterAnAgentCommandAndKeepsTheError(t *testing.T) {
	s := syncedSandbox(t)
	s.fake.OnQuery = func(sql string) bqtest.QueryScript {
		if strings.HasPrefix(sql, "bad") {
			return bqtest.QueryScript{DryRunErr: bqtest.QueryError("Syntax error: Unexpected identifier at [1:1]")}
		}
		return bqtest.Numbers(1)
	}
	warning := failLogClose(s)

	code, out, errOut := s.run("query", "select 1")
	if code != api.ExitOK {
		t.Fatalf("exit %d %s", code, errOut)
	}
	oneDocument(t, out)
	if errOut != warning+"\n" {
		t.Fatalf("want exactly the warning on stderr, got %q", errOut)
	}

	code, out, errOut = s.run("query", "bad sql")
	lines := stderrLines(errOut)
	if code != api.ExitBigQuery || out != "" || len(lines) != 2 || lines[0] != warning {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	if e := errBody(t, lines[1]); e.Code != "query" || !strings.Contains(e.Message, "[1:1]") {
		t.Fatalf("the error keeps its body: %+v", e)
	}
}

func TestFailedLogCloseWarnsOnlyAfterTheUIQuits(t *testing.T) {
	s := newSandbox(t)
	s.writeConfig("projects = [\"proj-a\"]\n")
	s.tty = true
	ran, during := false, ""
	s.ui = func(stderr string) { ran, during = true, stderr }

	if code, _, errOut := s.run("tui"); code != api.ExitOK || errOut != "" || !ran {
		t.Fatalf("a clean close prints nothing: exit %d, stderr %q, ui ran %v", code, errOut, ran)
	}

	warning := failLogClose(s)
	ran = false
	code, _, errOut := s.run("tui")
	if code != api.ExitOK || !ran {
		t.Fatalf("exit %d, ui ran %v", code, ran)
	}
	if during != "" {
		t.Fatalf("nothing reaches stderr while the UI runs, got %q", during)
	}
	if errOut != warning+"\n" {
		t.Fatalf("want exactly the warning on stderr after the UI, got %q", errOut)
	}
}
