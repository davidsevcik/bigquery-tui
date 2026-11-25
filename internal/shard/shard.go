package shard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"regexp/syntax"
	"sort"
	"strings"
	"time"

	"github.com/davidsevcik/bigquery-tui/internal/api"
)

type Pattern struct {
	Source string
	re     *regexp.Regexp
}

type Patterns struct {
	list []Pattern
}

type PatternError struct {
	Index   int
	Pattern string
	Reason  string
}

func (e PatternError) Error() string {
	return fmt.Sprintf("shard_patterns[%d] %q: %s", e.Index, e.Pattern, e.Reason)
}

func Compile(src []string) (*Patterns, []PatternError) {
	p := &Patterns{}
	var errs []PatternError
	for i, s := range src {
		re, err := regexp.Compile(anchor(s))
		if err != nil {
			reason := err.Error()
			var se *syntax.Error
			if errors.As(err, &se) {
				reason = fmt.Sprintf("not a valid RE2 expression (%s)", se.Code)
			}
			errs = append(errs, PatternError{Index: i, Pattern: s, Reason: reason})
			continue
		}
		if n := re.NumSubexp(); n != 1 {
			errs = append(errs, PatternError{Index: i, Pattern: s, Reason: fmt.Sprintf("needs exactly one capturing group to mark the varying part, found %d (write other groups as (?:…))", n)})
			continue
		}
		p.list = append(p.list, Pattern{Source: s, re: re})
	}
	return p, errs
}

func anchor(s string) string { return "^(?:" + s + ")$" }

func (p *Patterns) Empty() bool { return p == nil || len(p.list) == 0 }

func (p *Patterns) Sources() []string {
	if p == nil {
		return []string{}
	}
	out := make([]string, len(p.list))
	for i, x := range p.list {
		out[i] = x.Source
	}
	return out
}

func (p *Patterns) Hash() string {
	b, _ := json.Marshal(p.Sources())
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (p *Patterns) Combined() string {
	if p.Empty() {
		return ""
	}
	parts := make([]string, len(p.list))
	for i, x := range p.list {
		parts[i] = anchor(x.Source)
	}
	return strings.Join(parts, "|")
}

func (p *Patterns) MatchesAny(name string) bool {
	if p == nil {
		return false
	}
	for _, x := range p.list {
		if x.re.MatchString(name) {
			return true
		}
	}
	return false
}

func (x Pattern) split(name string) (before, varying, after string, ok bool) {
	m := x.re.FindStringSubmatchIndex(name)
	if m == nil || m[2] < 0 {
		return "", "", "", false
	}
	return name[:m[2]], name[m[2]:m[3]], name[m[3]:], true
}

func Assign(tables []api.TableMeta, p *Patterns) {
	for i := range tables {
		tables[i].Shard = nil
	}
	if p.Empty() {
		return
	}
	type key struct {
		dataset, before, after string
		typ                    api.TableType
	}
	assigned := make([]bool, len(tables))
	for _, pat := range p.list {
		groups := map[key][]int{}
		varying := map[int]string{}
		var order []key
		for i, t := range tables {
			if assigned[i] {
				continue
			}
			before, v, after, ok := pat.split(t.Name)
			if !ok {
				continue
			}
			k := key{t.Dataset, before, after, t.Type}
			if _, seen := groups[k]; !seen {
				order = append(order, k)
			}
			groups[k] = append(groups[k], i)
			varying[i] = v
		}
		for _, k := range order {
			idx := groups[k]
			if len(idx) < 2 {
				continue
			}
			fam := family(tables, idx, varying, k.before, k.after)
			for _, i := range idx {
				assigned[i] = true
				tables[i].Shard = fam
			}
		}
	}
}

func family(tables []api.TableMeta, idx []int, varying map[int]string, prefix, suffix string) *api.ShardFamily {
	f := &api.ShardFamily{Prefix: prefix, Suffix: suffix, Count: len(idx), Date: true}
	for _, i := range idx {
		if !ValidDate(varying[i]) {
			f.Date = false
			break
		}
	}
	if f.Date {
		f.Name = prefix + "YYYYMMDD" + suffix
		f.First, f.Last = varying[idx[0]], varying[idx[0]]
		newest := idx[0]
		for _, i := range idx[1:] {
			v := varying[i]
			if v < f.First {
				f.First = v
			}
			if v > f.Last {
				f.Last, newest = v, i
			}
		}
		f.Newest, f.NewestCreated = tables[newest].Name, tables[newest].CreatedAt
		return f
	}
	f.Name = prefix + "*" + suffix
	newest := idx[0]
	for _, i := range idx[1:] {
		if Newer(tables[i].CreatedAt, tables[i].Name, tables[newest].CreatedAt, tables[newest].Name) {
			newest = i
		}
	}
	f.Newest, f.NewestCreated = tables[newest].Name, tables[newest].CreatedAt
	return f
}

func Newer(aCreated time.Time, aName string, bCreated time.Time, bName string) bool {
	if !aCreated.Equal(bCreated) {
		return aCreated.After(bCreated)
	}
	return aName > bName
}

func SortNewestFirst[T any](f api.ShardFamily, items []T, name func(T) string, created func(T) time.Time) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if f.Date {
			return name(a) > name(b)
		}
		return Newer(created(a), name(a), created(b), name(b))
	})
}

func ValidDate(s string) bool {
	if len(s) != 8 {
		return false
	}
	t, err := time.Parse("20060102", s)
	if err != nil {
		return false
	}
	return t.Year() >= 1990 && t.Year() <= 2100
}
