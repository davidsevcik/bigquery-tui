package sqlx

import (
	"strings"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

// QuoteTable writes a table path as GoogleSQL: one pair of backticks around
// the whole path. A shard family path (the table ends in YYYYMMDD) becomes
// the wildcard `p.d.prefix*`.
func QuoteTable(p api.ObjectPath) string {
	if prefix, ok := strings.CutSuffix(p.Table, "YYYYMMDD"); ok {
		return "`" + p.Project + "." + p.Dataset + "." + prefix + "*`"
	}
	return "`" + p.String() + "`"
}

// SelectStar is the SELECT the Explorer's s writes for a table: one clause per
// line, and a WHERE condition the table accepts. It returns the text and the
// byte offset of the condition, where the cursor goes. A shard family (Path is
// the family path, prefix + YYYYMMDD, and Table.Shard is set) reads its newest
// shard through _TABLE_SUFFIX. A table that requires a partition filter gets a
// one-day filter on its partition column; everything else gets `true`.
func SelectStar(t api.TableDetail) (text string, cursor int) {
	from := QuoteTable(t.Path)
	cond := "true"
	switch sh := shard(t); {
	case sh != nil:
		cond = "_TABLE_SUFFIX = '" + sh.Last + "'"
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
	if sh == nil || sh.Last == "" || !strings.HasSuffix(t.Path.Table, "YYYYMMDD") {
		return nil
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
