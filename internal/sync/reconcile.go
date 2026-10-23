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
)

// Reconciliation is the outcome of ReconcileConfig.
type Reconciliation struct {
	Changed    bool
	Pruned     store.PruneStats
	SyncNeeded bool
	Reason     string
}

// ReconcileConfig brings a catalog in line with the effective ignore filter
// without any BigQuery call. If the filter hash changed, everything the new
// filter excludes is pruned locally. A sync is flagged as needed when the
// ignore list was loosened (anything other than appending ignore rules),
// since the catalog may then be incomplete. Selection never prunes: a
// deselected project keeps its cache. The caller must hold the sync lock.
func ReconcileConfig(ctx context.Context, cat *store.Catalog, cfg *config.Config) (Reconciliation, error) {
	var r Reconciliation
	cur := cfg.Filter()
	hash := cur.Hash()
	old, hadHash, err := cat.Meta(ctx, MetaConfigHash)
	if err != nil {
		return r, err
	}
	if hadHash && old == hash {
		needed, reason, err := syncNeeded(ctx, cat)
		r.SyncNeeded, r.Reason = needed, reason
		return r, err
	}

	if !hadHash {
		// First use of this catalog: nothing to prune, a sync is needed.
		r.Changed = true
		r.SyncNeeded, r.Reason = true, "catalog is empty"
	} else {
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
		switch {
		case !config.Tightening(oldFilter.Ignore, cur.Ignore):
			r.SyncNeeded, r.Reason = true, "ignore patterns changed"
		default:
			// pure tightening: pruning is enough; keep any
			// earlier "sync needed" flag.
			needed, reason, err := syncNeeded(ctx, cat)
			if err != nil {
				return r, err
			}
			r.SyncNeeded, r.Reason = needed, reason
		}
	}

	filterJSON, _ := json.Marshal(cur)
	if err := cat.SetMeta(ctx, MetaConfigHash, hash); err != nil {
		return r, err
	}
	if err := cat.SetMeta(ctx, MetaFilter, string(filterJSON)); err != nil {
		return r, err
	}
	if err := setSyncNeeded(ctx, cat, r.SyncNeeded, r.Reason); err != nil {
		return r, err
	}
	return r, nil
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
// for a different filter than cfg's, and why a sync is (or is not) needed.
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
