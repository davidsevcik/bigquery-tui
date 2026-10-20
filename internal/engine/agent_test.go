package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
	"github.com/davidsevcik/bigquery-tui/internal/bq"
	"github.com/davidsevcik/bigquery-tui/internal/bq/bqtest"
	"github.com/davidsevcik/bigquery-tui/internal/policy"
)

// agent opens a read-only engine for an agent entry point over the synced
// catalog, as `bqt query` or `bqt mcp` do.
func (e *env) agent(source string) *Engine {
	e.t.Helper()
	eng, err := Open(ctx, Options{
		Config: e.cfg, ReadOnly: true, Source: source,
		Jobs: func(context.Context) (bq.Jobs, error) { return e.fake, nil },
		Now:  func() time.Time { return e.now },
	})
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { eng.Close() })
	return eng
}

func TestAgentQueryReturnsAPageAndAResultID(t *testing.T) {
	e, _ := synced(t, `["proj-a"]`)
	e.fake.OnQuery = func(string) bqtest.QueryScript {
		s := bqtest.Numbers(250)
		s.Rows[3][1] = strings.Repeat("x", 300)
		s.Rows[4][1] = nil
		return s
	}
	eng := e.agent("mcp")
	res, err := eng.RunAgentQuery(ctx, AgentQuery{SQL: "select n, label from t", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 5 || res.TotalRows != 250 || !res.Truncated || res.NextCursor != "5" || res.ResultID == "" || res.StatementType != "SELECT" {
		t.Fatalf("%+v", res)
	}
	if c := res.Rows[3][1]; c == nil || len([]rune(*c)) != policy.CellChars+1 || !strings.HasSuffix(*c, "…") || res.TruncatedCells != 1 {
		t.Fatalf("cell cap: %v", res.Rows[3][1])
	}
	if res.Rows[4][1] != nil {
		t.Fatal("NULL is null")
	}
	if res.Stats == nil || res.Stats.BytesProcessed != 25000 {
		t.Fatalf("%+v", res.Stats)
	}
	labels := e.fake.QueryCalls()[1].Req.Labels
	if labels["bqt_source"] != "mcp" || labels["bqt"] != "1" {
		t.Fatalf("audit labels: %v", labels)
	}
	// the next page, from another process, by the result ID alone
	other := e.agent("cli")
	next, err := other.AgentResults(ctx, res.ResultID, 248, 50)
	if err != nil || len(next.Rows) != 2 || next.Truncated || next.StartIndex != 248 || *next.Rows[1][1] != "row 249" {
		t.Fatalf("%v %+v", err, next)
	}
	// History: one entry, source mcp, and the list offers the result
	hl, err := other.HistoryList(ctx, "source:mcp", 10, 0)
	if err != nil || len(hl.Items) != 1 || hl.Items[0].ResultID != res.ResultID || hl.Items[0].Source != "mcp" {
		t.Fatalf("%v %+v", err, hl)
	}
}

func TestAgentLimitsAndRefusals(t *testing.T) {
	e, _ := synced(t, `["proj-a"]`)
	eng := e.agent("mcp")
	e.fake.OnQuery = func(sql string) bqtest.QueryScript {
		if strings.HasPrefix(sql, "delete") {
			return bqtest.QueryScript{StatementType: "DELETE"}
		}
		s := bqtest.Numbers(2000)
		s.Bytes = 2 << 30
		return s
	}
	_, err := eng.RunAgentQuery(ctx, AgentQuery{SQL: "delete from t where true"})
	var pe *policy.Error
	if !errors.As(err, &pe) || pe.Refusal.Code != api.RefuseNotReadOnly {
		t.Fatalf("%v", err)
	}
	_, err = eng.RunAgentQuery(ctx, AgentQuery{SQL: "select big", MaxBytes: 1 << 40})
	if !errors.As(err, &pe) || pe.Refusal.Code != api.RefuseOverLimit || pe.Refusal.Limit != 1<<30 {
		t.Fatalf("a higher max_bytes argument cannot raise the limit: %v", err)
	}
	dr, err := eng.AgentDryRun(ctx, AgentQuery{SQL: "select big"})
	if err != nil || dr.Allowed || dr.Refusal == nil || dr.Bytes != 2<<30 || dr.Limit != 1<<30 {
		t.Fatalf("%v %+v", err, dr)
	}
	for _, c := range e.fake.QueryCalls() {
		if !c.Dry {
			t.Fatalf("no refused query may start a job: %+v", c)
		}
	}
	e.fake.OnQuery = func(string) bqtest.QueryScript { return bqtest.Numbers(2000) }
	res, err := eng.RunAgentQuery(ctx, AgentQuery{SQL: "select many", Limit: 5000})
	if err != nil || len(res.Rows) != policy.MaxRows {
		t.Fatalf("rows are capped at %d: %v %d", policy.MaxRows, err, len(res.Rows))
	}
	e.fake.OnQuery = func(string) bqtest.QueryScript {
		return bqtest.QueryScript{DryRunErr: bqtest.QueryError("Syntax error: Unexpected end of script at [1:7]")}
	}
	_, err = eng.RunAgentQuery(ctx, AgentQuery{SQL: "select"})
	var qf *QueryFailure
	if !errors.As(err, &qf) || qf.Err.Line != 1 || qf.Err.Col != 7 {
		t.Fatalf("%v", err)
	}
}

func TestAgentQueryCancelledByTheCaller(t *testing.T) {
	e, _ := synced(t, `["proj-a"]`)
	eng := e.agent("cli")
	block := make(chan struct{})
	defer close(block)
	e.fake.OnQuery = func(string) bqtest.QueryScript { s := bqtest.Numbers(1); s.Block = block; return s }
	c, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	_, err := eng.RunAgentQuery(c, AgentQuery{SQL: "select slow"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
}

func TestResultIDRoundTrip(t *testing.T) {
	for _, ref := range []api.JobRef{{Project: "p", Location: "EU", ID: "job_1"}, {Project: "example.com:p", Location: "us-central1", ID: "a-b_c"}} {
		got, err := ParseResultID(ResultID(ref))
		if err != nil || got != ref {
			t.Fatalf("%v %+v", err, got)
		}
	}
	for _, bad := range []string{"", "p", "p:EU", "p:EU.", ":EU.j", "p:.j", "p:EU.j.k"} {
		if _, err := ParseResultID(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestSchemaSearchAndPaths(t *testing.T) {
	e, _ := synced(t, `["proj-a"]`)
	eng := e.agent("mcp")
	s, err := eng.Schema(ctx, "`proj-a.sales.orders`")
	if err != nil || s.Table != "proj-a.sales.orders" || len(s.Columns) != 2 || s.Columns[0].Name != "id" || s.Location != "EU" {
		t.Fatalf("%v %+v", err, s)
	}
	if s, err := eng.Schema(ctx, "proj-a.sales.events_*"); err != nil || s.Shard == nil || s.Table != "proj-a.sales.events_20240102" {
		t.Fatalf("a shard family answers with its newest member: %v %+v", err, s)
	}
	e.cfg.ExecutionProject = "proj-a"
	if _, err := eng.Schema(ctx, "sales.orders"); err != nil {
		t.Fatalf("dataset.table in the Execution project: %v", err)
	}
	if _, err := eng.Schema(ctx, "proj-a.sales.nope"); !errors.Is(err, ErrUnknownTable) {
		t.Fatalf("%v", err)
	}
	hits, err := eng.CatalogSearch(ctx, "events", 10, 0)
	if err != nil || len(hits.Hits) != 1 || hits.Hits[0].Path != "proj-a.sales.events_*" || hits.Hits[0].Shard.Count != 2 {
		t.Fatalf("%v %+v", err, hits)
	}
	hits, _ = eng.CatalogSearch(ctx, "type:nonsense orders", 1, 0)
	if len(hits.Ignored) != 1 {
		t.Fatalf("an invalid filter is reported: %+v", hits)
	}
	all, _ := eng.CatalogSearch(ctx, "col:amount", 1, 0)
	if len(all.Hits) != 1 || all.Hits[0].Columns[0] != "amount STRING" {
		t.Fatalf("%+v", all)
	}
}

func TestFavoriteListAndBindParams(t *testing.T) {
	e, _ := synced(t, `["proj-a"]`)
	eng := e.agent("cli")
	dir := eng.Config().FavoritesDir
	os.MkdirAll(filepath.Join(dir, "team"), 0o755)
	os.WriteFile(filepath.Join(dir, "team", "dau.sql"), []byte("-- description: Daily users\n-- tags: events\n-- param: start DATE = 2024-01-01\n-- param: country STRING\nSELECT 1 WHERE @start > '2000-01-01' AND @country = 'x' AND @n > 0\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "other.sql"), []byte("SELECT 2\n"), 0o644)
	l, err := eng.FavoriteList("", 1, 0, false)
	if err != nil || l.Total != 2 || len(l.Favorites) != 1 || !l.Truncated || l.NextCursor != "1" || l.Favorites[0].SQL != "" {
		t.Fatalf("%v %+v", err, l)
	}
	l, _ = eng.FavoriteList("daily", 10, 0, true)
	if len(l.Favorites) != 1 || l.Favorites[0].ID != "team/dau.sql" || !strings.Contains(l.Favorites[0].SQL, "@country") || len(l.Favorites[0].Params) != 3 {
		t.Fatalf("%+v", l)
	}
	f, _ := eng.ReadFavorite("team/dau")
	if _, err := BindParams("", &f, map[string]string{"country": "CZ"}); err == nil || !strings.Contains(err.Error(), "@n") {
		t.Fatalf("a parameter without a value or default: %v", err)
	}
	ps, err := BindParams("", &f, map[string]string{"COUNTRY": "CZ", "n": "5"})
	if err != nil || len(ps) != 3 || ps[0].Type != "DATE" || ps[0].Value != "2024-01-01" || ps[2].Type != "INT64" {
		t.Fatalf("%v %+v", err, ps)
	}
	if _, err := BindParams("", &f, map[string]string{"country": "CZ", "n": "1", "typo": "x"}); err == nil || !strings.Contains(err.Error(), "typo") {
		t.Fatalf("an unused parameter is an error: %v", err)
	}
	if _, err := BindParams("", &f, map[string]string{"country": "CZ", "n": "1", "start": "soon"}); err == nil {
		t.Fatal("a bad DATE is an error")
	}
	if ps, err := BindParams("select @x", nil, map[string]string{"x": "DATE:2024-01-01"}); err != nil || ps[0].Type != "DATE" {
		t.Fatalf("%v %+v", err, ps)
	}
}
