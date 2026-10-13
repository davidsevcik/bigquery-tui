// Package api holds the plain data types shared by the core and every
// frontend. Everything here serializes to JSON, contains no rendered strings,
// and imports nothing UI-related. Offsets, where they appear, are UTF-8 byte
// offsets.
package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// TableType is the normalized table kind used in the catalog and in the
// `type:` search filter.
type TableType string

const (
	TypeTable    TableType = "table"
	TypeView     TableType = "view"
	TypeMView    TableType = "mview"
	TypeExternal TableType = "external"
	TypeSnapshot TableType = "snapshot"
	TypeClone    TableType = "clone"
	TypeOther    TableType = "other"
)

// ParseTableType maps INFORMATION_SCHEMA.TABLES.table_type (and the REST
// `type` field) to a TableType.
func ParseTableType(s string) TableType {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "BASE TABLE", "TABLE":
		return TypeTable
	case "VIEW":
		return TypeView
	case "MATERIALIZED VIEW", "MATERIALIZED_VIEW":
		return TypeMView
	case "EXTERNAL":
		return TypeExternal
	case "SNAPSHOT":
		return TypeSnapshot
	case "CLONE":
		return TypeClone
	default:
		return TypeOther
	}
}

// DatasetStatus says how complete a dataset's catalog rows are.
type DatasetStatus string

const (
	DatasetOK           DatasetStatus = "ok"
	DatasetNamesOnly    DatasetStatus = "names_only"
	DatasetInaccessible DatasetStatus = "inaccessible"
)

// DatasetMeta describes one dataset.
type DatasetMeta struct {
	Project      string            `json:"project"`
	Name         string            `json:"name"`
	Location     string            `json:"location"`
	Description  string            `json:"description,omitempty"`
	FriendlyName string            `json:"friendly_name,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
}

// ColumnMeta is one field path of a table (nested STRUCT fields get their own
// row with a dotted path).
type ColumnMeta struct {
	FieldPath         string `json:"field_path"`
	Ordinal           int    `json:"ordinal"`
	DataType          string `json:"data_type,omitempty"`
	Description       string `json:"description,omitempty"`
	Partitioning      bool   `json:"partitioning,omitempty"`
	ClusteringOrdinal int    `json:"clustering_ordinal,omitempty"`
}

// TopLevel reports whether the field is a top-level column.
func (c ColumnMeta) TopLevel() bool { return !strings.Contains(c.FieldPath, ".") }

// ParentPath returns the dotted parent path ("" for a top-level column).
func (c ColumnMeta) ParentPath() string {
	i := strings.LastIndex(c.FieldPath, ".")
	if i < 0 {
		return ""
	}
	return c.FieldPath[:i]
}

// TableMeta describes one table, view or similar object.
type TableMeta struct {
	Dataset     string            `json:"dataset"`
	Name        string            `json:"name"`
	Type        TableType         `json:"type"`
	CreatedAt   time.Time         `json:"created_at,omitempty"`
	Description string            `json:"description,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	ExpiresAt   *time.Time        `json:"expires_at,omitempty"`
	ViewSQL     string            `json:"view_sql,omitempty"`
	NamesOnly   bool              `json:"names_only,omitempty"`
	Columns     []ColumnMeta      `json:"columns,omitempty"`
}

// ContentHash is a stable hash of everything in the table except the shard
// family, used for the local diff.
func (t TableMeta) ContentHash() string {
	cp := t
	cols := append([]ColumnMeta(nil), t.Columns...)
	sort.Slice(cols, func(i, j int) bool { return cols[i].FieldPath < cols[j].FieldPath })
	cp.Columns = cols
	b, _ := json.Marshal(cp) // map keys are sorted by encoding/json
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Snapshot is what a metadata source returns for one (project, location)
// unit, or for a single dataset in the fallback ladder.
type Snapshot struct {
	Datasets []DatasetMeta `json:"datasets"`
	Tables   []TableMeta   `json:"tables"`
}

// ShardFamily describes a group of date-sharded tables (`events_YYYYMMDD`).
type ShardFamily struct {
	Prefix string `json:"prefix"`
	First  string `json:"first"`
	Last   string `json:"last"`
	Count  int    `json:"count"`
}
