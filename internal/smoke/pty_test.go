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
			_, _ = out.Write(buf[:n])
			if err != nil {
				return
			}
		}
	}()

	waitFor(t, out, "smoke-exec", 10*time.Second)
	waitFor(t, out, "space select", 5*time.Second)
	if _, err := f.Write([]byte("\x1b")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, out, "ctrl+g starts a short tutorial", 5*time.Second)
	if _, err := f.Write([]byte("?")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, out, "quit (asks if Runs are in flight)", 5*time.Second)
	if _, err := f.Write([]byte("\x1b")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if _, err := f.Write([]byte("\x0f")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, out, "i inserts", 5*time.Second)
	if _, err := f.Write([]byte("iselect q")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	if _, err := f.Write([]byte("\x11")); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("exit: %v", err)
		}
	case <-time.After(10 * time.Second):
		if err := cmd.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		t.Fatal("the UI did not quit on ctrl+q")
	}
	if _, err := os.Stat(filepath.Join(dir, "data", "catalog.db")); err != nil {
		t.Fatalf("the UI creates its catalog: %v", err)
	}
	drafts, _ := filepath.Glob(filepath.Join(dir, "data", "drafts", "tui", "*.sql"))
	if len(drafts) != 1 {
		t.Fatalf("drafts: %v", drafts)
	}
	if b, _ := os.ReadFile(drafts[0]); string(b) != "select q" {
		t.Fatalf("draft = %q", b)
	}
}
