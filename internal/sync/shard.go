package sync

import (
	"regexp"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

var shardRe = regexp.MustCompile(`^(.+?)(\d{8})$`)

// AssignShards detects date-sharded table families (`events_YYYYMMDD`) and
// sets Shard on every member. A family is two or more tables of the same type
// in one dataset sharing a prefix and an 8-digit date suffix.
func AssignShards(tables []api.TableMeta) {
	type key struct {
		prefix string
		typ    api.TableType
	}
	type fam struct {
		idx         []int
		first, last string
	}
	fams := map[key]*fam{}
	for i, t := range tables {
		m := shardRe.FindStringSubmatch(t.Name)
		if m == nil || !validDate(m[2]) {
			continue
		}
		k := key{m[1], t.Type}
		f := fams[k]
		if f == nil {
			f = &fam{first: m[2], last: m[2]}
			fams[k] = f
		}
		f.idx = append(f.idx, i)
		if m[2] < f.first {
			f.first = m[2]
		}
		if m[2] > f.last {
			f.last = m[2]
		}
	}
	for k, f := range fams {
		if len(f.idx) < 2 {
			continue
		}
		sf := &api.ShardFamily{Prefix: k.prefix, First: f.first, Last: f.last, Count: len(f.idx)}
		for _, i := range f.idx {
			tables[i].Shard = sf
		}
	}
}

func validDate(s string) bool {
	t, err := time.Parse("20060102", s)
	if err != nil {
		return false
	}
	return t.Year() >= 1990 && t.Year() <= 2100
}
