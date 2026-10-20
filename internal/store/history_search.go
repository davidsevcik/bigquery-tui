package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

// HistoryQuery is a resolved History search: free terms over the SQL and
// conditions that all must hold.
type HistoryQuery struct {
	Terms []api.QueryTerm
	Conds []HistoryCond
}

// HistoryCond is one filter. Field is "state" or "source" (Values match
// exactly), "project" (Values are substrings of the job or Execution
// project) or "since" (Since is the earliest creation time).
type HistoryCond struct {
	Field   string
	Values  []string
	Since   time.Time
	Negated bool
}

// NormalizeSQL collapses whitespace outside string literals, quoted
// identifiers and comments, and drops a trailing semicolon, so the same
// query typed twice groups as one History entry while 'a  b' and 'a b' stay
// apart.
func NormalizeSQL(s string) string {
	var b strings.Builder
	space := false
	var quote byte // ' " or ` while inside one
	comment := ""  // "--" (to the line end) or "/*" while inside one
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case comment == "--":
			b.WriteByte(c)
			if c == '\n' {
				comment = ""
			}
			continue
		case comment == "/*":
			b.WriteByte(c)
			if c == '*' && i+1 < len(s) && s[i+1] == '/' {
				b.WriteByte('/')
				i++
				comment = ""
			}
			continue
		case quote != 0:
			b.WriteByte(c)
			if c == '\\' && quote != '`' && i+1 < len(s) {
				b.WriteByte(s[i+1])
				i++
			} else if c == quote {
				quote = 0
			}
			continue
		}
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v' {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		switch {
		case c == '\'' || c == '"' || c == '`':
			quote = c
		case c == '#', c == '-' && i+1 < len(s) && s[i+1] == '-':
			comment = "--"
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			comment = "/*"
		}
		b.WriteByte(c)
	}
	out := b.String()
	if comment == "" && quote == 0 {
		out = strings.TrimRight(out, "; ")
	}
	return out
}

// SQLKey is the grouping key of a SQL text.
func SQLKey(s string) string {
	sum := sha256.Sum256([]byte(NormalizeSQL(s)))
	return hex.EncodeToString(sum[:16])
}

// fillKeys computes sql_key for rows written before the column existed.
func (h *History) fillKeys() error {
	var has int
	if err := h.db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('history') WHERE name='sql_key'").Scan(&has); err != nil || has == 0 {
		return err
	}
	rows, err := h.db.Query("SELECT id, sql FROM history WHERE sql_key IS NULL")
	if err != nil {
		return err
	}
	type row struct {
		id  int64
		sql string
	}
	var todo []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.sql); err != nil {
			rows.Close()
			return err
		}
		todo = append(todo, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(todo) == 0 {
		return err
	}
	tx, err := h.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, r := range todo {
		if _, err := tx.Exec("UPDATE history SET sql_key=? WHERE id=?", SQLKey(r.sql), r.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const entryCols = `h.id,h.project,h.location,h.job_id,h.execution_project,h.sql,h.statement_type,h.destination_table,h.state,h.error_message,
h.total_bytes_processed,h.total_bytes_billed,h.cache_hit,h.created_at,h.started_at,h.ended_at,h.duration_ms,h.total_slot_ms,
h.reservation_id,h.edition,h.source,h.favorite_path,h.params`

type scanner interface{ Scan(...any) error }

func scanEntry(s scanner, extra ...any) (api.HistoryEntry, error) {
	var e api.HistoryEntry
	var state, params string
	var cache int
	var created int64
	var started, ended sql.NullInt64
	var processed, billed, dur, slot sql.NullInt64
	dst := []any{&e.ID, &e.Job.Project, &e.Job.Location, &e.Job.ID, &e.ExecutionProject, &e.SQL, &e.StatementType, &e.Destination, &state, &e.Error,
		&processed, &billed, &cache, &created, &started, &ended, &dur, &slot,
		&e.ReservationID, &e.Edition, &e.Source, &e.FavoritePath, &params}
	if err := s.Scan(append(dst, extra...)...); err != nil {
		return e, err
	}
	e.State, e.CacheHit, e.Created = api.RunState(state), cache != 0, time.Unix(created, 0)
	e.BytesProcessed, e.BytesBilled, e.DurationMs, e.SlotMs = nullInt(processed), nullInt(billed), nullInt(dur), nullInt(slot)
	if started.Valid {
		t := time.Unix(started.Int64, 0)
		e.Started = &t
	}
	if ended.Valid {
		t := time.Unix(ended.Int64, 0)
		e.Ended = &t
	}
	if params != "" {
		if err := json.Unmarshal([]byte(params), &e.Params); err != nil {
			return e, fmt.Errorf("history entry %d: parameters: %w", e.ID, err)
		}
	}
	return e, nil
}

func nullInt(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}

// HistoryEntry returns one entry by ID.
func (h *History) HistoryEntry(ctx context.Context, id int64) (api.HistoryEntry, error) {
	return scanEntry(h.db.QueryRowContext(ctx, "SELECT "+entryCols+" FROM history h WHERE h.id=?", id))
}

// SetFavoritePath records that an entry was promoted to a Favorite.
func (h *History) SetFavoritePath(ctx context.Context, id int64, fav string) error {
	res, err := h.db.ExecContext(ctx, "UPDATE history SET favorite_path=? WHERE id=?", fav, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// Prune deletes the entries created before t and returns how many went.
func (h *History) Prune(ctx context.Context, before time.Time) (int64, error) {
	res, err := h.db.ExecContext(ctx, "DELETE FROM history WHERE created_at < ?", before.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SearchHistory returns one group per SQL text, newest first. Free terms
// match the SQL through full-text search (each term as a prefix; a term with
// punctuation matches as a phrase).
// SearchHistory returns one page of groups, whether more follow, and the
// number of groups that match (0 when the page is past the end).
func (h *History) SearchHistory(ctx context.Context, q HistoryQuery, limit, offset int) ([]api.HistoryGroup, bool, int, error) {
	if limit <= 0 {
		limit = 50
	}
	var where []string
	var args []any
	var fts []string
	for _, t := range q.Terms {
		if !hasWordChar(t.Text) {
			continue
		}
		fts = append(fts, `"`+strings.ReplaceAll(t.Text, `"`, `""`)+`"*`)
	}
	if len(fts) > 0 {
		where = append(where, "h.id IN (SELECT rowid FROM history_fts WHERE history_fts MATCH ?)")
		args = append(args, strings.Join(fts, " "))
	}
	for _, c := range q.Conds {
		var cond string
		var cargs []any
		switch c.Field {
		case "state", "source":
			if len(c.Values) == 0 {
				continue
			}
			cond = "h." + c.Field + " IN (" + strings.TrimSuffix(strings.Repeat("?,", len(c.Values)), ",") + ")"
			for _, v := range c.Values {
				cargs = append(cargs, v)
			}
		case "project":
			if len(c.Values) == 0 {
				continue
			}
			var ors []string
			for _, v := range c.Values {
				ors = append(ors, `(lower(h.project) LIKE ? ESCAPE '\' OR lower(h.execution_project) LIKE ? ESCAPE '\')`)
				pat := "%" + likeEscape(v) + "%"
				cargs = append(cargs, pat, pat)
			}
			cond = "(" + strings.Join(ors, " OR ") + ")"
		case "since":
			cond, cargs = "h.created_at >= ?", []any{c.Since.Unix()}
		default:
			return nil, false, 0, fmt.Errorf("history: unknown filter %q", c.Field)
		}
		if c.Negated {
			cond = "NOT " + cond
		}
		where = append(where, cond)
		args = append(args, cargs...)
	}
	filter := ""
	if len(where) > 0 {
		filter = "WHERE " + strings.Join(where, " AND ")
	}
	query := `WITH m AS (SELECT h.* FROM history h ` + filter + `),
k AS (SELECT m.*, COALESCE(sql_key, 'id:' || id) AS k,
  ROW_NUMBER() OVER (PARTITION BY COALESCE(sql_key, 'id:' || id) ORDER BY created_at DESC, id DESC) AS rn FROM m),
g AS (SELECT k, COUNT(*) AS n,
  SUM(CASE WHEN reservation_id = '' THEN COALESCE(total_bytes_billed, 0) ELSE 0 END) AS billed,
  SUM(CASE WHEN reservation_id != '' THEN COALESCE(total_slot_ms, 0) ELSE 0 END) AS slot
  FROM k GROUP BY k),
r AS (SELECT id, k FROM k WHERE rn = 1)
SELECT ` + entryCols + `, g.n, g.billed, g.slot, g.k, COUNT(*) OVER () FROM g JOIN r ON r.k = g.k JOIN history h ON h.id = r.id
ORDER BY h.created_at DESC, h.id DESC LIMIT ? OFFSET ?`
	args = append(args, limit+1, offset)
	rows, err := h.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, 0, err
	}
	defer rows.Close()
	out := []api.HistoryGroup{}
	total := 0
	for rows.Next() {
		var g api.HistoryGroup
		e, err := scanEntry(rows, &g.Runs, &g.BytesBilled, &g.SlotMs, &g.Key, &total)
		if err != nil {
			return nil, false, 0, err
		}
		g.Latest = e
		g.Spans = termSpans(e.SQL, q.Terms)
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, false, 0, err
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	return out, more, total, nil
}

func hasWordChar(s string) bool {
	for _, r := range s {
		if r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r > 127 {
			return true
		}
	}
	return false
}

func likeEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(strings.ToLower(s))
}

// termSpans marks every case-insensitive occurrence of the terms (byte
// offsets; only ASCII letters are folded, so offsets stay valid).
func termSpans(text string, terms []api.QueryTerm) []api.Span {
	lower := asciiLower(text)
	var out []api.Span
	for _, t := range terms {
		needle := asciiLower(t.Text)
		if needle == "" {
			continue
		}
		for i := 0; ; {
			j := strings.Index(lower[i:], needle)
			if j < 0 {
				break
			}
			out = append(out, api.Span{Start: i + j, End: i + j + len(needle)})
			i += j + len(needle)
		}
	}
	return mergeSpans(out)
}

func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

func mergeSpans(sp []api.Span) []api.Span {
	if len(sp) < 2 {
		return sp
	}
	for i := 1; i < len(sp); i++ {
		for j := i; j > 0 && sp[j].Start < sp[j-1].Start; j-- {
			sp[j], sp[j-1] = sp[j-1], sp[j]
		}
	}
	out := []api.Span{sp[0]}
	for _, s := range sp[1:] {
		last := &out[len(out)-1]
		if s.Start <= last.End {
			last.End = max(last.End, s.End)
			continue
		}
		out = append(out, s)
	}
	return out
}
