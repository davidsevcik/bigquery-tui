package sqlx

import (
	"strings"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

// QuoteTable writes a table path as GoogleSQL: one pair of backticks around
// the whole path, as written. A table's name may hold YYYYMMDD itself
// (report_YYYYMMDD_backup), so only QuoteFamily, given a family, writes a
// wildcard.
func QuoteTable(p api.ObjectPath) string { return "`" + p.String() + "`" }

// QuoteFamily writes a shard family's path as its wildcard `p.d.prefix*`:
// BigQuery allows the * only at the end. f may be nil when only the family's
// name is known; its prefix is then the text before the name's marker.
func QuoteFamily(p api.ObjectPath, f *api.ShardFamily) string {
	prefix, ok := "", false
	if f != nil {
		prefix, ok = f.Prefix, true
	} else {
		prefix, ok = FamilyPrefix(p.Table)
	}
	if !ok {
		return QuoteTable(p)
	}
	return "`" + p.Project + "." + p.Dataset + "." + prefix + "*`"
}

// IsFamilyPath reports whether name names the family f itself (its name or
// its wildcard) rather than one of its members.
func IsFamilyPath(name string, f *api.ShardFamily) bool {
	return f != nil && (name == f.Name || name == f.Wildcard())
}

// FamilyPrefix returns the prefix of a shard family name: the text before its
// * or YYYYMMDD marker. Use it only on names known to be family names.
func FamilyPrefix(name string) (string, bool) {
	p, _, ok := FamilyParts(name)
	return p, ok
}

// FamilyParts splits a shard family name into the text before and after its
// marker: a member is prefix + varying part + suffix. A table name cannot
// hold *, so a * is the marker when there is one; otherwise it is the last
// YYYYMMDD (the prefix may hold that text itself).
func FamilyParts(name string) (prefix, suffix string, ok bool) {
	i, n := strings.Index(name, "*"), 1
	if i < 0 {
		i, n = strings.LastIndex(name, "YYYYMMDD"), len("YYYYMMDD")
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
		from = QuoteFamily(t.Path, sh)
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
	if !IsFamilyPath(t.Path.Table, sh) {
		return nil // a member, or a plain table: it reads itself
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
