package sqlx

import (
	"strings"

	"github.com/davidsevcik/bigquery-tui/pkg/api"
)

func QuoteTable(p api.ObjectPath) string { return "`" + p.String() + "`" }

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

func IsFamilyPath(name string, f *api.ShardFamily) bool {
	return f != nil && (name == f.Name || name == f.Wildcard())
}

func FamilyPrefix(name string) (string, bool) {
	p, _, ok := FamilyParts(name)
	return p, ok
}

func FamilyParts(name string) (prefix, suffix string, ok bool) {
	i, n := strings.Index(name, "*"), 1
	if i < 0 {
		i, n = strings.Index(name, "YYYYMMDD"), len("YYYYMMDD")
	}
	if i < 0 {
		return "", "", false
	}
	return name[:i], name[i+n:], true
}

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
		return nil
	}
	return sh
}

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
