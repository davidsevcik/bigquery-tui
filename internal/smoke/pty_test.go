// Package smoke starts the real binary in a pseudo-terminal: the one test
// that exercises terminal setup, the alternate screen, key input and a clean
// exit together (ticket 16).
package smoke

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

type capture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *capture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

func (c *capture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

func waitFor(t *testing.T, c *capture, want string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if strings.Contains(c.String(), want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("never saw %q in the terminal output (%d bytes)", want, len(c.String()))
}

func TestTUIStartsRendersAndQuitsInAPTY(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the binary")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bqt")
	if out, err := exec.Command("go", "build", "-o", bin, "../../cmd/bqt").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	cfg := filepath.Join(dir, "config.toml")
	// No projects: the UI must come up without credentials or network.
	if err := os.WriteFile(cfg, []byte("data_dir = \""+filepath.Join(dir, "data")+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "--config", cfg)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "HOME="+dir, "BQT_EXECUTION_PROJECT=smoke-exec")
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 90})
	if err != nil {
		t.Skipf("no pty available: %v", err)
	}
	defer f.Close()
	out := &capture{}
	go func() {
		buf := make([]byte, 32<<10)
		for {
			n, err := f.Read(buf)
			out.Write(buf[:n])
			if err != nil {
				return
			}
		}
	}()

	waitFor(t, out, "smoke-exec", 10*time.Second)
	// nothing selected: the Projects dialog opens first; closing it shows
	// the first-run hint
	waitFor(t, out, "space select", 5*time.Second)
	f.Write([]byte("\x1b"))
	waitFor(t, out, "ctrl+g starts a short tutorial", 5*time.Second)
	f.Write([]byte("?"))
	waitFor(t, out, "command palette", 5*time.Second)
	f.Write([]byte("\x1b")) // esc closes the help
	time.Sleep(150 * time.Millisecond)
	// ctrl+o goes to the editor; printable keys type there (q must not quit)
	f.Write([]byte("\x0f"))
	waitFor(t, out, "i inserts", 5*time.Second)
	f.Write([]byte("iselect q")) // i: the editor starts in vim's Normal mode
	// the renderer only sends changed cells, so there is no stable text to
	// wait for: the draft file checked below proves the keys arrived
	time.Sleep(500 * time.Millisecond)
	f.Write([]byte("\x11")) // ctrl+q quits from anywhere

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("exit: %v", err)
		}
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		t.Fatal("the UI did not quit on ctrl+q")
	}
	if _, err := os.Stat(filepath.Join(dir, "data", "catalog.db")); err != nil {
		t.Fatalf("the UI creates its catalog: %v", err)
	}
	// the buffer was autosaved as a draft for the next start
	drafts, _ := filepath.Glob(filepath.Join(dir, "data", "drafts", "tui", "*.sql"))
	if len(drafts) != 1 {
		t.Fatalf("drafts: %v", drafts)
	}
	if b, _ := os.ReadFile(drafts[0]); string(b) != "select q" {
		t.Fatalf("draft = %q", b)
	}
}
