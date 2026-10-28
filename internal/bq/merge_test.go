package bq

import "testing"

func TestMergeSnapshotMarksRequiredTopLevelColumns(t *testing.T) {
	k := tkey{"d", "t"}
	snap := mergeSnapshot(nil, []tableRow{{key: k, typ: "BASE TABLE"}}, nil, nil,
		[]colRow{{key: k, col: "id", ordinal: 1, required: true}, {key: k, col: "addr", ordinal: 2, required: true}, {key: k, col: "name", ordinal: 3}},
		[]fieldRow{{key: k, col: "id", path: "id", typ: "INT64"}, {key: k, col: "addr", path: "addr", typ: "STRUCT<city STRING>"},
			{key: k, col: "addr", path: "addr.city", typ: "STRING"}, {key: k, col: "name", path: "name", typ: "STRING"}})
	got := map[string]bool{}
	for _, c := range snap.Tables[0].Columns {
		got[c.FieldPath] = c.Required
	}
	// the metadata views carry no mode for nested fields
	if !got["id"] || !got["addr"] || got["addr.city"] || got["name"] {
		t.Fatalf("%v", got)
	}
}

func TestMergeSnapshotReadsTheRequiredPartitionFilterOption(t *testing.T) {
	var tables []tableRow
	var opts []optRow
	for name, val := range map[string]string{"lower": "true", "upper": "TRUE", "padded": " true ", "off": "false"} {
		k := tkey{"d", name}
		tables = append(tables, tableRow{key: k, typ: "BASE TABLE"})
		opts = append(opts, optRow{k, "require_partition_filter", val})
	}
	tables = append(tables, tableRow{key: tkey{"d", "none"}, typ: "BASE TABLE"})
	snap := mergeSnapshot(nil, tables, opts, nil, nil, nil)
	got := map[string]bool{}
	for _, tb := range snap.Tables {
		got[tb.Name] = tb.RequirePartitionFilter
	}
	want := map[string]bool{"lower": true, "upper": true, "padded": true, "off": false, "none": false}
	for n, w := range want {
		if got[n] != w {
			t.Errorf("%s: got %v want %v", n, got[n], w)
		}
	}
}
