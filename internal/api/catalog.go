package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

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

type DatasetStatus string

const (
	DatasetOK           DatasetStatus = "ok"
	DatasetNamesOnly    DatasetStatus = "names_only"
	DatasetInaccessible DatasetStatus = "inaccessible"
	DatasetPending      DatasetStatus = "pending"
)

type DatasetMeta struct {
	Project      string            `json:"project"`
	Name         string            `json:"name"`
	Location     string            `json:"location"`
	Description  string            `json:"description,omitempty"`
	FriendlyName string            `json:"friendly_name,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
}

type ColumnMeta struct {
	FieldPath         string `json:"field_path"`
	Ordinal           int    `json:"ordinal"`
	DataType          string `json:"data_type,omitempty"`
	Description       string `json:"description,omitempty"`
	Partitioning      bool   `json:"partitioning,omitempty"`
	ClusteringOrdinal int    `json:"clustering_ordinal,omitempty"`
	Required          bool   `json:"required,omitempty"`
}

func (c ColumnMeta) TopLevel() bool { return !strings.Contains(c.FieldPath, ".") }

func (c ColumnMeta) ParentPath() string {
	i := strings.LastIndex(c.FieldPath, ".")
	if i < 0 {
		return ""
	}
	return c.FieldPath[:i]
}

type TableMeta struct {
	Dataset                string            `json:"dataset"`
	Name                   string            `json:"name"`
	Type                   TableType         `json:"type"`
	CreatedAt              time.Time         `json:"created_at,omitempty"`
	Description            string            `json:"description,omitempty"`
	Labels                 map[string]string `json:"labels,omitempty"`
	ExpiresAt              *time.Time        `json:"expires_at,omitempty"`
	ViewSQL                string            `json:"view_sql,omitempty"`
	NamesOnly              bool              `json:"names_only,omitempty"`
	RequirePartitionFilter bool              `json:"require_partition_filter,omitempty"`
	Columns                []ColumnMeta      `json:"columns,omitempty"`
	Shard                  *ShardFamily      `json:"shard,omitempty"`
	SharedSchema           bool              `json:"shared_schema,omitempty"`
	OwnSchema              bool              `json:"own_schema,omitempty"`
}

func (t TableMeta) ContentHash() string {
	cp := t
	cp.Shard, cp.SharedSchema, cp.OwnSchema = nil, false, false
	cols := append([]ColumnMeta(nil), t.Columns...)
	sort.Slice(cols, func(i, j int) bool { return cols[i].FieldPath < cols[j].FieldPath })
	cp.Columns = cols
	b, _ := json.Marshal(cp)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

type Snapshot struct {
	Datasets []DatasetMeta `json:"datasets"`
	Tables   []TableMeta   `json:"tables"`
}

type ShardFamily struct {
	Name          string    `json:"name"`
	Prefix        string    `json:"prefix"`
	Suffix        string    `json:"suffix,omitempty"`
	Date          bool      `json:"date,omitempty"`
	First         string    `json:"first,omitempty"`
	Last          string    `json:"last,omitempty"`
	Count         int       `json:"count"`
	Newest        string    `json:"newest"`
	NewestCreated time.Time `json:"newest_created,omitzero"`
}

func (f ShardFamily) Wildcard() string { return f.Prefix + "*" }

func (f ShardFamily) TableSuffix(member string) string { return strings.TrimPrefix(member, f.Prefix) }

func (f ShardFamily) Varying(member string) (string, bool) {
	if len(member) < len(f.Prefix)+len(f.Suffix) || !strings.HasPrefix(member, f.Prefix) || !strings.HasSuffix(member, f.Suffix) {
		return "", false
	}
	return member[len(f.Prefix) : len(member)-len(f.Suffix)], true
}

func (f ShardFamily) AgentName() string {
	if f.Date && f.Suffix == "" {
		return f.Wildcard()
	}
	return f.Name
}
