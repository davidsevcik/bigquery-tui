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
