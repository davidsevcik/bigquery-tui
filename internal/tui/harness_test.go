package tui

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq"
	"github.com/davidsevcik/bigquery-tui/internal/bq/bqtest"
	"github.com/davidsevcik/bigquery-tui/internal/config"
	"github.com/davidsevcik/bigquery-tui/internal/engine"
)

var update = flag.Bool("update", false, "rewrite the golden screen files")

var testNow = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

type rig struct {
	t    *testing.T
	m    *Model
	fake *bqtest.Fake
	eng  *engine.Engine
	dir  string
	cfg  string
	now  time.Time
}

func nested(name string) api.TableMeta {
	t := bqtest.Table(name, "id", "customer_id", "amount", "address", "address.city", "address.geo", "address.geo.lat", "created_at")
	t.Description = "All customer orders, one row per order"
	t.Columns[0].Description = "Order id"
	t.Columns[0].DataType = "INT64"
	t.Columns[3].DataType = "STRUCT<city STRING, geo STRUCT<lat FLOAT64>>"
	t.Columns[7].DataType = "TIMESTAMP"
	t.Columns[7].Partitioning = true
	t.Labels = map[string]string{"team": "sales"}
	return t
}

func fixtureFake() *bqtest.Fake {
	f := bqtest.New()
	a := f.AddProject("acme-prod")
	view := bqtest.Table("v_orders")
	view.Type, view.ViewSQL = api.TypeView, "SELECT *\nFROM `acme-prod.sales.orders`\nWHERE amount > 0"
	events := []api.TableMeta{bqtest.Table("clicks", "user_id", "url")}
	for _, d := range []string{"20240101", "20240102", "20240103", "20240104"} {
		events = append(events, bqtest.Table("events_"+d, "user_id", "event_name"))
	}
	a.Datasets = []bqtest.Dataset{
		bqtest.DS("sales", "EU", nested("orders"), bqtest.Table("order_items", "order_id", "sku", "qty"), bqtest.Table("customers", "id", "email"), view),
		bqtest.DS("events", "EU", events...),
		bqtest.DS("finance", "EU", bqtest.Table("refunds", "id", "amount"), bqtest.Table("invoices", "id")),
		bqtest.DS("us_archive", "US", bqtest.Table("orders_2019", "id")),
	}
	b := f.AddProject("acme-staging")
	b.Datasets = []bqtest.Dataset{bqtest.DS("sales", "EU", bqtest.Table("orders", "id"))}
	f.TableData = map[string]bqtest.TableData{
		"acme-prod.sales.orders": {
			Info: bq.TableInfo{Type: api.TypeTable, Bytes: 5 << 20, Schema: []bq.Field{{Name: "id", Type: "INTEGER", Mode: "REQUIRED"}, {Name: "customer_id", Type: "STRING"}, {Name: "amount", Type: "NUMERIC"}}},
		},
	}
	var rows [][]any
	for i := 1; i <= 45; i++ {
		var amount any = "19.99"
		if i%7 == 0 {
			amount = nil
		}
		rows = append(rows, []any{strings.Repeat("1", 1) + string(rune('0'+i%10)), "cust-" + string(rune('a'+i%26)), amount})
	}
	td := f.TableData["acme-prod.sales.orders"]
	td.Rows = rows
	f.TableData["acme-prod.sales.orders"] = td
	return f
}

func newRig(t *testing.T, w, h int) *rig {
	t.Helper()
	dir := t.TempDir()
	r := &rig{t: t, fake: fixtureFake(), dir: dir, now: testNow}
	r.cfg = filepath.Join(dir, "config.toml")
	src := "execution_project = \"acme-exec\"\ndata_dir = \"" + filepath.Join(dir, "data") + "\"\nprojects = [\"acme-prod\", \"acme-staging\"]\n"
	if err := os.WriteFile(r.cfg, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	load := config.LoadOptions{Path: r.cfg, Getenv: func(string) string { return "" }}
	cfg, err := config.Load(load)
	if err != nil {
		t.Fatal(err)
	}
	eng, err := engine.Open(context.Background(), engine.Options{
		Config:  cfg,
		Meta:    func(context.Context) (bq.Metadata, error) { return r.fake, nil },
		Tables:  func(context.Context) (bq.Tables, error) { return r.fake, nil },
		Now:     func() time.Time { return r.now },
		Backoff: func(int) time.Duration { return 0 },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	// first sync happened three hours before "now"
	r.now = testNow.Add(-3 * time.Hour)
	hd, err := eng.StartSync(context.Background(), api.SyncScope{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	r.now = testNow
	if err := eng.ReloadIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.eng = eng
	r.m = New(EngineBackend{Engine: eng, Load: load}, Options{NoBackground: true, StableOutput: true, Now: func() time.Time { return r.now }, Getenv: func(string) string { return "" }})
	t.Cleanup(r.m.Close)
	r.send(tea.WindowSizeMsg{Width: w, Height: h})
	r.run(r.m.Init())
	return r
}

// run executes a command and everything it leads to, synchronously.
func (r *rig) run(cmd tea.Cmd) {
	r.t.Helper()
	for depth := 0; cmd != nil; depth++ {
		if depth > 200 {
			r.t.Fatal("command chain does not settle")
		}
		msg := cmd()
		switch x := msg.(type) {
		case nil:
			return
		case tea.BatchMsg:
			for _, c := range x {
				r.run(c)
			}
			return
		case tea.QuitMsg:
			return
		}
		_, cmd = r.m.Update(msg)
	}
}

func (r *rig) send(msg tea.Msg) {
	r.t.Helper()
	_, cmd := r.m.Update(msg)
	r.run(cmd)
}

var special = map[string]rune{
	"enter": tea.KeyEnter, "esc": tea.KeyEscape, "tab": tea.KeyTab, "up": tea.KeyUp, "down": tea.KeyDown,
	"left": tea.KeyLeft, "right": tea.KeyRight, "backspace": tea.KeyBackspace, "f1": tea.KeyF1, "pgdown": tea.KeyPgDown, "pgup": tea.KeyPgUp,
}

// press sends keys: a name such as "enter" or "ctrl+p", or literal text.
func (r *rig) press(keys ...string) {
	r.t.Helper()
	for _, k := range keys {
		switch {
		case special[k] != 0:
			r.send(tea.KeyPressMsg{Code: special[k]})
		case strings.HasPrefix(k, "ctrl+") && len(k) == 6:
			r.send(tea.KeyPressMsg{Code: rune(k[5]), Mod: tea.ModCtrl})
		default:
			for _, ch := range k {
				r.send(tea.KeyPressMsg{Code: ch, Text: string(ch)})
			}
		}
	}
}

func (r *rig) screen() string {
	lines := r.m.render()
	for i, l := range lines {
		lines[i] = strings.TrimRight(ansi.Strip(l), " ")
	}
	return strings.Join(lines, "\n") + "\n"
}

func (r *rig) contains(want ...string) {
	r.t.Helper()
	s := r.screen()
	for _, w := range want {
		if !strings.Contains(s, w) {
			r.t.Fatalf("screen does not contain %q:\n%s", w, s)
		}
	}
}

func (r *rig) lacks(not ...string) {
	r.t.Helper()
	s := r.screen()
	for _, w := range not {
		if strings.Contains(s, w) {
			r.t.Fatalf("screen must not contain %q:\n%s", w, s)
		}
	}
}

func (r *rig) golden(name string) {
	r.t.Helper()
	// every line must be exactly as wide as the terminal
	for i, l := range r.m.render() {
		if wd := ansi.StringWidth(l); wd != r.m.w {
			r.t.Errorf("%s: line %d is %d cells wide, terminal is %d: %q", name, i, wd, r.m.w, ansi.Strip(l))
		}
	}
	if n := len(r.m.render()); n != r.m.h {
		r.t.Errorf("%s: %d lines for a %d-row terminal", name, n, r.m.h)
	}
	path := filepath.Join("testdata", name+".txt")
	got := r.screen()
	if *update {
		os.MkdirAll("testdata", 0o755)
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			r.t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		r.t.Fatalf("missing golden file %s (run go test ./internal/tui -update): %v", path, err)
	}
	if string(want) != got {
		r.t.Errorf("%s differs from the golden file (run with -update after checking):\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}
