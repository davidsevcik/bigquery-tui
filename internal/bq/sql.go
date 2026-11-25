package bq

import (
	"fmt"
	"strings"
)

type scope struct {
	project string
	region  string
	dataset string
}

func regionScope(project, location string) scope {
	return scope{project: project, region: "region-" + strings.ToLower(location)}
}

func datasetScope(project, dataset string) scope {
	return scope{project: project, dataset: dataset}
}

func quoteIdent(s string) string {
	return "`" + strings.ReplaceAll(s, "`", "\\`") + "`"
}

func (s scope) view(name string) string {
	q := s.region
	if q == "" {
		q = s.dataset
	}
	return fmt.Sprintf("%s.%s.INFORMATION_SCHEMA.%s", quoteIdent(s.project), quoteIdent(q), name)
}

func (s scope) filter(col string) string {
	if s.region != "" {
		return fmt.Sprintf("%s IN UNNEST(@ds)", col)
	}
	return "TRUE"
}

func (s scope) datasetsSQL() (string, bool) {
	if s.region == "" {
		return "", false
	}
	return fmt.Sprintf(`SELECT schema_name, option_name, option_value
FROM %s
WHERE %s AND option_name IN ('description', 'friendly_name', 'labels')`, s.view("SCHEMATA_OPTIONS"), s.filter("schema_name")), true
}

func (s scope) tablesSQL() string {
	return fmt.Sprintf(`SELECT table_schema, table_name, table_type, creation_time
FROM %s
WHERE %s`, s.view("TABLES"), s.filter("table_schema"))
}

func (s scope) tableOptionsSQL() string {
	return fmt.Sprintf(`SELECT table_schema, table_name, option_name, option_value
FROM %s
WHERE %s AND option_name IN ('description', 'labels', 'expiration_timestamp', 'require_partition_filter')`, s.view("TABLE_OPTIONS"), s.filter("table_schema"))
}

func (s scope) viewsSQL() string {
	return fmt.Sprintf(`SELECT table_schema, table_name, view_definition
FROM %s
WHERE %s`, s.view("VIEWS"), s.filter("table_schema"))
}

func (s scope) columnsSQL(where string) string {
	return fmt.Sprintf(`SELECT table_schema, table_name, column_name, ordinal_position, is_partitioning_column, clustering_ordinal_position, is_nullable, data_type
FROM %s
WHERE %s AND %s`, s.view("COLUMNS"), s.filter("table_schema"), where)
}

func (s scope) fieldPathsSQL(where string) string {
	return fmt.Sprintf(`SELECT table_schema, table_name, column_name, field_path, data_type, description
FROM %s
WHERE %s AND %s`, s.view("COLUMN_FIELD_PATHS"), s.filter("table_schema"), where) + readInOrder
}

const readInOrder = "\n-- read in order, as if ORDER BY: one Storage Read stream"
