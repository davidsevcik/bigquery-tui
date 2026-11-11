package sync

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/davidsevcik/bigquery-tui/internal/config"
	"github.com/davidsevcik/bigquery-tui/internal/store"
)

// Meta keys written by sync and reconcile.
const (
	MetaConfigHash = "config_hash"
	MetaFilter     = "config_filter"
	MetaSyncNeeded = "sync_needed"
	MetaSyncReason = "sync_reason"
	// MetaShardHash is the hash of the shard patterns the catalog's families
	// were computed with. A catalog without it was built by an older bqt,
	// whose built-in rule collapsed date-suffixed tables.
	MetaShardHash = "shard_patterns_hash"
)

// Reasons for "sync needed" that the shard patterns give.
const (
	ReasonShardPatterns = "shard patterns changed"
	// ReasonStaleFamilies: a family's newest shard is gone from the catalog
	// (a tightened ignore list pruned it, or an older bqt wrote the catalog),
	// so its members have no schema to show until a sync.
	ReasonStaleFamilies = "shard families out of date"
	// ReasonDateRuleGone is the one-time notice after upgrading from the
	// built-in date rule: it names the pattern that restores it.
	ReasonDateRuleGone = `date-suffixed tables no longer collapse by themselves: add shard_patterns = ['.+(\d{8})'] to the config to keep events_YYYYMMDD families`
)

// Reconciliation is the outcome of ReconcileConfig.
type Reconciliation struct {
	Changed    bool
	Pruned     store.PruneStats
	SyncNeeded bool
	Reason     string
}

// ReconcileConfig brings a catalog in line with the effective ignore filter
// and shard patterns without any BigQuery call. If the filter hash changed,
// everything the new filter excludes is pruned locally. A sync is flagged as
// needed when the ignore list was loosened (anything other than appending
// ignore rules), since the catalog may then be incomplete, and when the shard
// patterns changed, since the families were computed with the old ones (the
// own-schema marks of gs go with them). Selection never prunes: a deselected
// project keeps its cache. The caller must hold the sync lock.
func ReconcileConfig(ctx context.Context, cat *store.Catalog, cfg *config.Config) (Reconciliation, error) {
	var r Reconciliation
	cur := cfg.Filter()
	hash := cur.Hash()
	old, hadHash, err := cat.Meta(ctx, MetaConfigHash)
	if err != nil {
		return r, err
	}
	sh, err := shardChange(ctx, cat, cfg, hadHash)
	if err != nil {
		return r, err
	}
	if hadHash && old == hash && !sh.changed {
		needed, reason, err := syncNeeded(ctx, cat)
		if err == nil && !needed {
			if needed, err = cat.StaleFamilies(ctx); needed && err == nil {
				reason = ReasonStaleFamilies
				err = setSyncNeeded(ctx, cat, true, reason)
			}
		}
		r.SyncNeeded, r.Reason = needed, reason
		return r, err
	}

	switch {
	case !hadHash:
		// First use of this catalog: nothing to prune, a sync is needed.
		r.Changed = true
		r.SyncNeeded, r.Reason = true, "catalog is empty"
	case old != hash:
		r.Changed = true
		var oldFilter config.Filter
		if raw, ok, _ := cat.Meta(ctx, MetaFilter); ok {
			_ = json.Unmarshal([]byte(raw), &oldFilter)
		}
		m := cfg.Matcher()
		r.Pruned, err = cat.Prune(ctx, nil, m.Ignored)
		if err != nil {
			return r, err
		}
		if !config.Tightening(oldFilter.Ignore, cur.Ignore) {
			r.SyncNeeded, r.Reason = true, "ignore patterns changed"
			break
		}
		fallthrough
	default:
		// pure tightening, or only the shard patterns changed: keep any
		// earlier "sync needed" flag.
		needed, reason, err := syncNeeded(ctx, cat)
		if err != nil {
			return r, err
		}
		r.SyncNeeded, r.Reason = needed, reason
	}
	if sh.changed {
		r.Changed = true
		if sh.clearMarks {
			if err := cat.ClearOwnSchema(ctx); err != nil {
				return r, err
			}
		}
		if sh.reason != "" && (!r.SyncNeeded || r.Reason == ReasonShardPatterns || r.Reason == ReasonDateRuleGone) {
			r.SyncNeeded, r.Reason = true, sh.reason
		}
	}

	if !r.SyncNeeded {
		stale, err := cat.StaleFamilies(ctx)
		if err != nil {
			return r, err
		}
		if stale {
			r.SyncNeeded, r.Reason = true, ReasonStaleFamilies
		}
	}

	filterJSON, _ := json.Marshal(cur)
	if err := cat.SetMeta(ctx, MetaConfigHash, hash); err != nil {
		return r, err
	}
	if err := cat.SetMeta(ctx, MetaFilter, string(filterJSON)); err != nil {
		return r, err
	}
	if err := cat.SetMeta(ctx, MetaShardHash, cfg.Shards().Hash()); err != nil {
		return r, err
	}
	if err := setSyncNeeded(ctx, cat, r.SyncNeeded, r.Reason); err != nil {
		return r, err
	}
	return r, nil
}

// shardDiff is how the shard patterns differ from the ones the catalog's
// families were computed with.
type shardDiff struct {
	changed    bool   // the stored hash must be updated
	reason     string // set when a sync is needed
	clearMarks bool   // the families are gone, and with them the gs marks
}

// shardChange compares the shard patterns with the catalog's. hadCatalog is
// false for a catalog no sync has used yet.
func shardChange(ctx context.Context, cat *store.Catalog, cfg *config.Config, hadCatalog bool) (shardDiff, error) {
	var d shardDiff
	cur := cfg.Shards().Hash()
	old, had, err := cat.Meta(ctx, MetaShardHash)
	switch {
	case err != nil:
		return d, err
	case had && old == cur:
		return d, nil
	case had:
		return shardDiff{changed: true, reason: ReasonShardPatterns, clearMarks: true}, nil
	case !hadCatalog:
		return shardDiff{changed: true}, nil // the empty catalog needs its sync anyway
	}
	// Built by a bqt that had the date rule built in.
	d.changed = true
	if !cfg.Shards().Empty() {
		d.reason = ReasonShardPatterns
		return d, nil
	}
	legacy, err := cat.HasLegacyDateFamilies(ctx)
	if legacy {
		d.reason = ReasonDateRuleGone
	}
	return d, err
}

func syncNeeded(ctx context.Context, cat *store.Catalog) (bool, string, error) {
	v, ok, err := cat.Meta(ctx, MetaSyncNeeded)
	if err != nil || !ok || v != "1" {
		return false, "", err
	}
	reason, _, err := cat.Meta(ctx, MetaSyncReason)
	return true, reason, err
}

func setSyncNeeded(ctx context.Context, cat *store.Catalog, needed bool, reason string) error {
	v := "0"
	if needed {
		v = "1"
	}
	if err := cat.SetMeta(ctx, MetaSyncNeeded, v); err != nil {
		return err
	}
	return cat.SetMeta(ctx, MetaSyncReason, reason)
}

// PendingConfigChange tells a read-only caller whether the catalog was built
// for a different filter or different shard patterns than cfg's, and why a
// sync is (or is not) needed.
func PendingConfigChange(ctx context.Context, cat *store.Catalog, cfg *config.Config) (bool, string, error) {
	old, ok, err := cat.Meta(ctx, MetaConfigHash)
	if err != nil {
		return false, "", err
	}
	if !ok {
		return true, "catalog is empty", nil
	}
	if old != cfg.Filter().Hash() && !sameIgnore(ctx, cat, cfg.Filter()) {
		return true, "config changed (ignore patterns)", nil
	}
	sh, err := shardChange(ctx, cat, cfg, true)
	if err != nil {
		return false, "", err
	}
	if sh.reason == ReasonShardPatterns {
		return true, "config changed (shard patterns)", nil
	}
	if sh.reason != "" {
		return true, sh.reason, nil
	}
	needed, reason, err := syncNeeded(ctx, cat)
	if err != nil {
		return false, "", err
	}
	if needed {
		return true, reason, nil
	}
	return false, "", nil
}

// sameIgnore reports whether the catalog was built with cur's ignore
// patterns. Catalogs from before the Projects dialog also stored the old
// `projects` list in the filter, so their hash differs although nothing that
// still matters changed.
func sameIgnore(ctx context.Context, cat *store.Catalog, cur config.Filter) bool {
	raw, ok, err := cat.Meta(ctx, MetaFilter)
	if err != nil || !ok {
		return false
	}
	var old config.Filter
	if json.Unmarshal([]byte(raw), &old) != nil {
		return false
	}
	if len(old.Ignore) != len(cur.Ignore) {
		return false
	}
	for i := range old.Ignore {
		if old.Ignore[i] != cur.Ignore[i] {
			return false
		}
	}
	return true
}

func (r Reconciliation) String() string {
	return fmt.Sprintf("changed=%v pruned=%+v syncNeeded=%v reason=%q", r.Changed, r.Pruned, r.SyncNeeded, r.Reason)
}
