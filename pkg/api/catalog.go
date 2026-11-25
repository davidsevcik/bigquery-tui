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
	// DatasetPending is a dataset BigQuery lists whose unit has never
	// committed it: it has no tables yet. Frontends show it as "syncing"
	// while this process's sync is reading its unit, "not synced" otherwise.
	DatasetPending DatasetStatus = "pending"
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
	// Required is the REQUIRED mode. The metadata views report it for
	// top-level columns only, so nested fields are always false here.
	Required bool `json:"required,omitempty"`
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
	// RequirePartitionFilter is the require_partition_filter table option:
	// queries must filter on the partition column. Unknown reads false.
	RequirePartitionFilter bool         `json:"require_partition_filter,omitempty"`
	Columns                []ColumnMeta `json:"columns,omitempty"`
	// Shard is set by sync when the table belongs to a shard family.
	Shard *ShardFamily `json:"shard,omitempty"`
	// SharedSchema marks a family member whose columns were not read: it
	// uses its family's schema (the newest shard's columns).
	SharedSchema bool `json:"shared_schema,omitempty"`
	// OwnSchema marks a family member whose own schema was synced on request
	// (gs, bqt sync --table): later syncs keep reading its columns.
	OwnSchema bool `json:"own_schema,omitempty"`
}

// ContentHash is a stable hash of everything in the table except the shard
// family and its schema marks, used for the local diff.
func (t TableMeta) ContentHash() string {
	cp := t
	cp.Shard, cp.SharedSchema, cp.OwnSchema = nil, false, false
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

// ShardFamily describes a group of tables whose names differ only in one
// varying part, as a shard pattern marks it (spec section 8.3). Members are
// the tables named Prefix + <varying part> + Suffix.
type ShardFamily struct {
	// Name is how the family is shown: Prefix + "YYYYMMDD" + Suffix for a
	// date family, Prefix + "*" + Suffix for any other.
	Name   string `json:"name"`
	Prefix string `json:"prefix"`
	Suffix string `json:"suffix,omitempty"`
	// Date is set when every member's varying part is a valid YYYYMMDD date.
	Date bool `json:"date,omitempty"`
	// First and Last are the oldest and newest dates (date families only).
	First string `json:"first,omitempty"`
	Last  string `json:"last,omitempty"`
	Count int    `json:"count"`
	// Newest is the newest shard: the latest date, or for other families the
	// latest creation time (ties broken by name). The family's schema is its
	// schema.
	Newest        string    `json:"newest"`
	NewestCreated time.Time `json:"newest_created,omitzero"`
}

// Wildcard is the BigQuery wildcard table over the family (Prefix + "*").
// BigQuery allows the * only at the end, so it also matches any other table
// whose name starts with Prefix.
func (f ShardFamily) Wildcard() string { return f.Prefix + "*" }

// TableSuffix is the _TABLE_SUFFIX value that selects one member through the
// wildcard: its name without the prefix.
func (f ShardFamily) TableSuffix(member string) string { return strings.TrimPrefix(member, f.Prefix) }

// Varying returns the varying part of a member's name.
func (f ShardFamily) Varying(member string) (string, bool) {
	if len(member) < len(f.Prefix)+len(f.Suffix) || !strings.HasPrefix(member, f.Prefix) || !strings.HasSuffix(member, f.Suffix) {
		return "", false
	}
	return member[len(f.Prefix) : len(member)-len(f.Suffix)], true
}

// AgentName is the family's table name for the CLI and MCP: the family name,
// except that a date family without a suffix keeps the wildcard it has
// always been reported as (events_*).
func (f ShardFamily) AgentName() string {
	if f.Date && f.Suffix == "" {
		return f.Wildcard()
	}
	return f.Name
}
