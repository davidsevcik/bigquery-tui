package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq"
	"github.com/davidsevcik/bigquery-tui/internal/bq/bqtest"
	"github.com/davidsevcik/bigquery-tui/internal/config"
	"github.com/davidsevcik/bigquery-tui/internal/engine"
)

var ctx = context.Background()

type rig struct {
	fake *bqtest.Fake
	cfg  *config.Config
	cs   *sdk.ClientSession
}

// newRig starts the server over the in-memory transport. With sync set the
// catalog is filled first, as a person's `bqt sync` would.
func newRig(t *testing.T, sync bool) *rig {
	t.Helper()
	dir := t.TempDir()
	cfg, err := config.Parse([]byte("execution_project = \"exec\"\ndata_dir = \""+dir+"\"\nprojects = [\"proj-a\"]\n"), "t.toml", config.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{fake: bqtest.New(), cfg: cfg}
	r.fake.AddProject("proj-a").Datasets = []bqtest.Dataset{bqtest.DS("sales", "EU", bqtest.Table("orders", "id", "amount"))}
	if sync {
		r.sync(t)
	}
	srv, err := New(ctx, func(ctx context.Context) (*engine.Engine, error) {
		return engine.Open(ctx, engine.Options{Config: cfg, ReadOnly: true, Source: "mcp",
			Jobs: func(context.Context) (bq.Jobs, error) { return r.fake, nil }})
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	st, ct := sdk.NewInMemoryTransports()
	ss, err := srv.MCP("test").Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	r.cs = cs
	return r
}

func (r *rig) sync(t *testing.T) {
	t.Helper()
	eng, err := engine.Open(ctx, engine.Options{Config: r.cfg, Meta: func(context.Context) (bq.Metadata, error) { return r.fake, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	h, err := eng.StartSync(ctx, api.SyncScope{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Wait(); err != nil {
		t.Fatal(err)
	}
}

// call runs a tool and decodes its structured output into out; it returns
// the error text of a tool error.
func (r *rig) call(t *testing.T, name string, args map[string]any, out any) string {
	t.Helper()
	res, err := r.cs.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error %v", name, err)
	}
	if res.IsError {
		return res.Content[0].(*sdk.TextContent).Text
	}
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("%s: %v\n%s", name, err, b)
	}
	return ""
}

func TestToolsAndInstructionsFitClaudeCode(t *testing.T) {
	r := newRig(t, true)
	tools, err := r.cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"search_catalog": true, "get_schema": true, "catalog_status": true, "dry_run": true,
		"run_query": true, "get_results": true, "list_favorites": true, "search_history": true}
	if len(tools.Tools) != len(want) {
		t.Fatalf("%d tools", len(tools.Tools))
	}
	for _, tl := range tools.Tools {
		if !want[tl.Name] {
			t.Errorf("unexpected tool %s", tl.Name)
		}
		if len(tl.Description) > 2048 {
			t.Errorf("%s: description is %d characters (Claude Code cuts at 2,048)", tl.Name, len(tl.Description))
		}
		if tl.Annotations == nil || !tl.Annotations.ReadOnlyHint {
			t.Errorf("%s: not marked read-only", tl.Name)
		}
	}
	if n := len(Instructions); n > 2048 {
		t.Fatalf("instructions are %d characters", n)
	}
	if got := r.cs.InitializeResult().Instructions; got != Instructions {
		t.Fatal("instructions not sent")
	}
}

func TestSearchSchemaQueryAndPage(t *testing.T) {
	r := newRig(t, true)
	var hits api.CatalogHits
	if e := r.call(t, "search_catalog", map[string]any{"query": "orders"}, &hits); e != "" || len(hits.Hits) != 1 || hits.Hits[0].Path != "proj-a.sales.orders" {
		t.Fatalf("%s %+v", e, hits)
	}
	var s api.TableSchema
	if e := r.call(t, "get_schema", map[string]any{"table": "proj-a.sales.orders"}, &s); e != "" || len(s.Columns) != 2 {
		t.Fatalf("%s %+v", e, s)
	}
	if e := r.call(t, "get_schema", map[string]any{"table": "proj-a.sales.nope"}, &s); !strings.Contains(e, "not in the catalog") {
		t.Fatalf("%q", e)
	}
	r.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.Numbers(150) }
	var dr api.DryRunResult
	if e := r.call(t, "dry_run", map[string]any{"sql": "select n from t where n > @min", "params": map[string]any{"min": "5"}}, &dr); e != "" || !dr.Allowed || dr.StatementType != "SELECT" {
		t.Fatalf("%s %+v", e, dr)
	}
	var res api.QueryResult
	if e := r.call(t, "run_query", map[string]any{"sql": "select n, label from t"}, &res); e != "" || len(res.Rows) != 100 || !res.Truncated || res.NextCursor != "100" {
		t.Fatalf("%s %+v", e, res)
	}
	var more api.QueryResult
	if e := r.call(t, "get_results", map[string]any{"result_id": res.ResultID, "cursor": res.NextCursor}, &more); e != "" || len(more.Rows) != 50 || more.Truncated || *more.Rows[49][1] != "row 149" {
		t.Fatalf("%s %+v", e, more)
	}
	var hl api.HistoryList
	if e := r.call(t, "search_history", map[string]any{"query": "label"}, &hl); e != "" || len(hl.Items) != 1 || hl.Items[0].Source != "mcp" {
		t.Fatalf("%s %+v", e, hl)
	}
	var fl api.FavoriteList
	if e := r.call(t, "list_favorites", map[string]any{}, &fl); e != "" || fl.Total != 0 {
		t.Fatalf("%s %+v", e, fl)
	}
	var st api.CatalogFreshness
	if e := r.call(t, "catalog_status", map[string]any{}, &st); e != "" || st.Tables != 1 || len(st.Projects) != 1 || st.SyncNeeded {
		t.Fatalf("%s %+v", e, st)
	}
}

func TestRefusalsAreToolErrors(t *testing.T) {
	r := newRig(t, true)
	r.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.QueryScript{StatementType: "DROP_TABLE"} }
	var res api.QueryResult
	e := r.call(t, "run_query", map[string]any{"sql": "drop table t"}, &res)
	if !strings.Contains(e, "not_read_only") || !strings.Contains(e, "only SELECT") {
		t.Fatalf("%q", e)
	}
	for _, c := range r.fake.QueryCalls() {
		if !c.Dry {
			t.Fatal("a refused statement started a job")
		}
	}
	r.fake.OnQuery = func(string) bqtest.QueryScript { s := bqtest.Numbers(1); s.Bytes = 5 << 30; return s }
	if e := r.call(t, "run_query", map[string]any{"sql": "select big", "max_bytes": int64(1) << 40}, &res); !strings.Contains(e, "over_limit") {
		t.Fatalf("a tool argument cannot raise the limit: %q", e)
	}
	if e := r.call(t, "run_query", map[string]any{"sql": "select @x"}, &res); !strings.Contains(e, "@x") {
		t.Fatalf("%q", e)
	}
	if e := r.call(t, "get_results", map[string]any{"result_id": "nonsense"}, &res); !strings.Contains(e, "result_id") {
		t.Fatalf("%q", e)
	}
}

func TestServerStartedBeforeTheFirstSync(t *testing.T) {
	r := newRig(t, false)
	var st api.CatalogFreshness
	if e := r.call(t, "catalog_status", map[string]any{}, &st); e != "" || !st.SyncNeeded || !strings.Contains(st.Advice, "bqt sync") {
		t.Fatalf("%s %+v", e, st)
	}
	var hits api.CatalogHits
	if e := r.call(t, "search_catalog", map[string]any{"query": "orders"}, &hits); !strings.Contains(e, "bqt sync") {
		t.Fatalf("%q", e)
	}
	r.sync(t) // the person syncs; the running server picks the catalog up
	deadline := time.Now().Add(5 * time.Second)
	for {
		e := r.call(t, "search_catalog", map[string]any{"query": "orders"}, &hits)
		if e == "" && len(hits.Hits) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%q %+v", e, hits)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
