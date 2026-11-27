package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const writerProcessEnv = "BQT_LOGGING_TEST_WRITER"

type writerSpec struct {
	Path    string        `json:"path"`
	Writer  int           `json:"writer"`
	Rounds  int           `json:"rounds"`
	Burst   int           `json:"burst"`
	Pause   time.Duration `json:"pause"`
	MaxSize int64         `json:"max_size"`
	Backups int           `json:"backups"`
}

func TestMain(m *testing.M) {
	if raw := os.Getenv(writerProcessEnv); raw != "" {
		if err := runWriterProcess(raw); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runWriterProcess(raw string) error {
	var s writerSpec
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return err
	}
	w := newRotatingFile(s.Path, s.MaxSize, s.Backups)
	for round := range s.Rounds {
		if err := writeRecords(w, s.Writer, round*s.Burst, s.Burst); err != nil {
			return err
		}
		time.Sleep(s.Pause)
	}
	return w.Close()
}

var recordPadding = strings.Repeat("x", 120)

func writeRecords(w io.Writer, writer, from, count int) error {
	h := slog.NewJSONHandler(w, nil)
	for seq := from; seq < from+count; seq++ {
		r := slog.NewRecord(time.Now(), slog.LevelInfo, "record", 0)
		r.AddAttrs(slog.Int("writer", writer), slog.Int("seq", seq), slog.String("pad", recordPadding))
		if err := h.Handle(context.Background(), r); err != nil {
			return err
		}
	}
	return nil
}

type recordKey struct{ writer, seq int }

type logFile struct {
	name    string
	size    int64
	mode    os.FileMode
	records []recordKey
	maxLine int
}

func readLogFiles(t *testing.T, dir string) (live logFile, backups []logFile) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if name != FileName && !(strings.HasPrefix(name, "bqt-") && strings.HasSuffix(name, ".log")) {
			continue
		}
		f := readLogFile(t, filepath.Join(dir, name))
		if name == FileName {
			live = f
		} else {
			backups = append(backups, f)
		}
	}
	if live.name == "" {
		t.Fatalf("no %s in %s", FileName, dir)
	}
	return live, backups
}

func readLogFile(t *testing.T, path string) logFile {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	f := logFile{name: filepath.Base(path), size: fi.Size(), mode: fi.Mode().Perm()}
	for line := range bytes.Lines(b) {
		f.maxLine = max(f.maxLine, len(line))
		var rec struct {
			Msg    string `json:"msg"`
			Writer *int   `json:"writer"`
			Seq    *int   `json:"seq"`
			Pad    string `json:"pad"`
		}
		if err := json.Unmarshal(line, &rec); err != nil || rec.Msg != "record" || rec.Writer == nil || rec.Seq == nil || rec.Pad != recordPadding {
			t.Fatalf("%s holds a damaged record %q: %v", f.name, line, err)
		}
		f.records = append(f.records, recordKey{*rec.Writer, *rec.Seq})
	}
	return f
}

func countRecords(files ...logFile) map[recordKey]int {
	seen := map[recordKey]int{}
	for _, f := range files {
		for _, k := range f.records {
			seen[k]++
		}
	}
	return seen
}

func requireEveryRecordOnce(t *testing.T, seen map[recordKey]int, written map[int]int) {
	t.Helper()
	total := 0
	for writer, n := range written {
		total += n
		for seq := range n {
			if got := seen[recordKey{writer, seq}]; got != 1 {
				t.Errorf("writer %d record %d found %d times", writer, seq, got)
			}
		}
	}
	if len(seen) != total {
		t.Errorf("found %d distinct records, wrote %d", len(seen), total)
	}
}

func TestConcurrentProcessesKeepEveryRecordAndRotateAtTheFileSize(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	const maxSize = 32 << 10
	specs := []writerSpec{
		{Writer: 0, Rounds: 150, Burst: 20, Pause: time.Millisecond},
		{Writer: 1, Rounds: 150, Burst: 5, Pause: time.Millisecond},
		{Writer: 2, Rounds: 150, Burst: 1, Pause: time.Millisecond},
	}
	cmds := make([]*exec.Cmd, len(specs))
	outputs := make([]bytes.Buffer, len(specs))
	for i := range specs {
		specs[i].Path, specs[i].MaxSize, specs[i].Backups = filepath.Join(dir, FileName), maxSize, 1<<20
		raw, err := json.Marshal(specs[i])
		if err != nil {
			t.Fatal(err)
		}
		cmds[i] = exec.Command(self)
		cmds[i].Env = append(os.Environ(), writerProcessEnv+"="+string(raw))
		cmds[i].Stdout, cmds[i].Stderr = &outputs[i], &outputs[i]
		if err := cmds[i].Start(); err != nil {
			t.Fatal(err)
		}
	}
	for i, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("writer %d: %v\n%s", i, err, outputs[i].String())
		}
	}

	live, backups := readLogFiles(t, dir)
	written := map[int]int{}
	for _, s := range specs {
		written[s.Writer] = s.Rounds * s.Burst
	}
	requireEveryRecordOnce(t, countRecords(append(backups, live)...), written)

	if len(backups) < 10 {
		t.Fatalf("only %d rotations; the test needs several", len(backups))
	}
	maxLine := live.maxLine
	for _, b := range backups {
		maxLine = max(maxLine, b.maxLine)
	}
	limit := int64(maxSize + len(specs)*maxLine)
	for _, f := range append(backups, live) {
		if f.size > limit {
			t.Errorf("%s is %d bytes, past the %d-byte limit by more than one record per writer", f.name, f.size, maxSize)
		}
	}
	for _, b := range backups {
		if b.size <= int64(maxSize-maxLine) {
			t.Errorf("%s was rotated at %d bytes, before the file reached the %d-byte limit", b.name, b.size, maxSize)
		}
	}
}

func TestQuietWriterFollowsTheLogAnotherWriterRotated(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	const maxSize = 4 << 10
	busy := newRotatingFile(path, maxSize, maxBackups)
	quiet := newRotatingFile(path, maxSize, maxBackups)
	t.Cleanup(func() {
		_ = busy.Close()
		_ = quiet.Close()
	})
	if err := writeRecords(quiet, 1, 0, 1); err != nil {
		t.Fatal(err)
	}
	if err := writeRecords(busy, 0, 0, 200); err != nil {
		t.Fatal(err)
	}
	if err := writeRecords(quiet, 1, 1, 5); err != nil {
		t.Fatal(err)
	}

	live, backups := readLogFiles(t, filepath.Dir(path))
	if len(backups) != maxBackups {
		t.Fatalf("%d backups kept, want %d", len(backups), maxBackups)
	}
	inLive := countRecords(live)
	for seq := 1; seq <= 5; seq++ {
		if inLive[recordKey{1, seq}] != 1 {
			t.Errorf("the quiet writer's record %d, written after the last rotation, is not in %s once", seq, FileName)
		}
	}
}

func TestGoroutinesSharingOneWriterKeepEveryRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	w := newRotatingFile(path, 4<<10, 1000)
	const goroutines, perGoroutine = 4, 100
	var wg sync.WaitGroup
	errs := make([]error, goroutines)
	for g := range goroutines {
		wg.Go(func() { errs[g] = writeRecords(w, g, 0, perGoroutine) })
	}
	wg.Wait()
	if err := errors.Join(append(errs, w.Close())...); err != nil {
		t.Fatal(err)
	}
	live, backups := readLogFiles(t, filepath.Dir(path))
	if len(backups) == 0 {
		t.Fatal("no rotation happened")
	}
	written := map[int]int{}
	for g := range goroutines {
		written[g] = perGoroutine
	}
	requireEveryRecordOnce(t, countRecords(append(backups, live)...), written)
}

func TestRotationsInTheSameMillisecondKeepDistinctBackups(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	stopped := time.Date(2026, 11, 27, 10, 0, 0, 123_456_789, time.UTC)
	writers := []*rotatingFile{newRotatingFile(path, 2<<10, 100), newRotatingFile(path, 2<<10, 100)}
	for _, w := range writers {
		w.now = func() time.Time { return stopped }
		t.Cleanup(func() { _ = w.Close() })
	}
	const perWriter = 40
	for seq := range perWriter {
		for i, w := range writers {
			if err := writeRecords(w, i, seq, 1); err != nil {
				t.Fatal(err)
			}
		}
	}

	live, backups := readLogFiles(t, filepath.Dir(path))
	if len(backups) < 3 {
		t.Fatalf("only %d backups from rotations at one instant", len(backups))
	}
	requireEveryRecordOnce(t, countRecords(append(backups, live)...), map[int]int{0: perWriter, 1: perWriter})
}

func TestRotatedBackupsArePrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no Unix modes")
	}
	path := filepath.Join(t.TempDir(), FileName)
	w := newRotatingFile(path, 2<<10, 100)
	t.Cleanup(func() { _ = w.Close() })
	if err := writeRecords(w, 0, 0, 40); err != nil {
		t.Fatal(err)
	}
	live, backups := readLogFiles(t, filepath.Dir(path))
	if len(backups) == 0 {
		t.Fatal("no rotation happened")
	}
	for _, f := range append(backups, live) {
		if f.mode != 0o600 {
			t.Errorf("%s: mode %o, want 600", f.name, f.mode)
		}
	}
}
