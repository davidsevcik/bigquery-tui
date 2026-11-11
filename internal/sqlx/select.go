package sqlx

import (
	"strings"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

// QuoteTable writes a table path as GoogleSQL: one pair of backticks around
// the whole path. A shard family path (its name holds YYYYMMDD or *, as in
// events_YYYYMMDD or orders_*_history) becomes the wildcard `p.d.prefix*`:
// BigQuery allows the * only at the end.
func QuoteTable(p api.ObjectPath) string {
	if prefix, ok := FamilyPrefix(p.Table); ok {
		return "`" + p.Project + "." + p.Dataset + "." + prefix + "*`"
	}
	return "`" + p.String() + "`"
}

// FamilyPrefix returns the prefix of a shard family name: the text before its
// YYYYMMDD or * marker. A table name holds neither.
func FamilyPrefix(name string) (string, bool) {
	p, _, ok := FamilyParts(name)
	return p, ok
}

// FamilyParts splits a shard family name into the text before and after its
// YYYYMMDD or * marker: a member is prefix + varying part + suffix.
func FamilyParts(name string) (prefix, suffix string, ok bool) {
	i, n := strings.Index(name, "*"), 1
	if j := strings.Index(name, "YYYYMMDD"); j >= 0 && (i < 0 || j < i) {
		i, n = j, len("YYYYMMDD")
	}
	if i < 0 {
		return "", "", false
	}
	return name[:i], name[i+n:], true
}

// SelectStar is the SELECT the Explorer's s writes for a table: one clause per
// line, and a WHERE condition the table accepts. It returns the text and the
// byte offset of the condition, where the cursor goes. A shard family (Path is
// the family path and Table.Shard is set) reads its newest shard through its
// wildcard, with _TABLE_SUFFIX pinned to that one shard. A table that requires
// a partition filter gets a one-day filter on its partition column; everything
// else gets `true`.
func SelectStar(t api.TableDetail) (text string, cursor int) {
	from := QuoteTable(t.Path)
	cond := "true"
	switch sh := shard(t); {
	case sh != nil:
		cond = "_TABLE_SUFFIX = '" + strings.ReplaceAll(sh.TableSuffix(sh.Newest), "'", "\\'") + "'"
	case t.Table.RequirePartitionFilter:
		cond = partitionFilter(t.Table.Columns)
	}
	head := "SELECT *\nFROM " + from + "\nWHERE "
	return head + cond + "\nLIMIT 100", len(head)
}

func shard(t api.TableDetail) *api.ShardFamily {
	sh := t.Table.Shard
	if sh == nil {
		sh = t.Shard
	}
	if sh == nil || sh.Newest == "" {
		return nil
	}
	if _, family := FamilyPrefix(t.Path.Table); !family {
		return nil // a member: it reads itself
	}
	return sh
}

// partitionFilter is a one-day filter on the first top-level partitioning
// column. Without one, the table is assumed partitioned by ingestion time.
func partitionFilter(cols []api.ColumnMeta) string {
	for _, c := range cols {
		if !c.Partitioning || !c.TopLevel() || c.FieldPath == "_PARTITIONTIME" || c.FieldPath == "_PARTITIONDATE" {
			continue
		}
		name := quoteColumn(c.FieldPath)
		switch strings.ToUpper(c.DataType) {
		case "DATE":
			return name + " >= DATE_SUB(CURRENT_DATE(), INTERVAL 1 DAY)"
		case "TIMESTAMP":
			return name + " >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 1 DAY)"
		case "DATETIME":
			return name + " >= DATETIME_SUB(CURRENT_DATETIME(), INTERVAL 1 DAY)"
		case "INT64", "INTEGER":
			return name + " BETWEEN 0 AND 0  -- set the range"
		}
		return "true"
	}
	return "_PARTITIONTIME >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 1 DAY)"
}

// quoteColumn writes a column name bare when it is a plain identifier that is
// not a keyword, and in backticks otherwise.
func quoteColumn(name string) string {
	plain := name != "" && isIdentStart(name[0]) && !keywords[strings.ToUpper(name)]
	for i := 0; plain && i < len(name); i++ {
		plain = isIdentByte(name[i])
	}
	if plain {
		return name
	}
	return "`" + name + "`"
}
