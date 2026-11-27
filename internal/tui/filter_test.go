package tui

import "testing"

func (r *rig) selected(want string) {
	r.t.Helper()
	if got := r.m.sel().path.String(); got != want {
		r.t.Fatalf("selection is %q, want %q\n%s", got, want, r.screen())
	}
}

func TestMillerFilterNarrowsTheFocusedColumn(t *testing.T) {
	r := newRig(t, 140, 30)
	r.press("l", "j", "j", "l")
	r.press("/", "ord")
	r.contains("filter tables ▸", "order_items", "v_orders")
	r.lacks("customers")
	r.selected("acme-prod.sales.orders")
	r.contains("finance", "us_archive")
	r.press("enter")
	r.lacks("filter tables ▸")
	r.contains("Tables 2/3 of 4 ▸ ord")
	r.press("k")
	r.selected("acme-prod.sales.order_items")
}

func TestMillerColumnFiltersAndTheLeftRule(t *testing.T) {
	r := newRig(t, 140, 30)
	r.press("l", "/", "s", "enter")
	r.contains("Datasets 2/3 of 4 ▸ s")
	r.lacks("finance")
	r.press("l", "/", "ord", "enter")
	r.contains("Datasets 2/3 of 4 ▸ s", "Tables 2/3 of 4 ▸ ord")
	r.press("esc")
	r.contains("Datasets 2/3 of 4 ▸ s", "Tables 3/4")
	r.selected("acme-prod.sales.orders")
	r.press("/", "zzz")
	r.contains("no match for “zzz”")
	r.press("enter")
	r.press("h")
	r.contains("Datasets 2/3 of 4 ▸ s")
	r.lacks("zzz")
	r.press("l")
	r.contains("Tables 1/4")
	r.press("h", "h")
	r.lacks("▸ s")
}

func TestMillerFilterKeepsShardFamiliesByTheirMembers(t *testing.T) {
	r := newRig(t, 160, 30)
	r.press("ctrl+p", "Layout: focus", "enter")
	r.press("l", "l", "/", "20240102", "enter")
	r.contains("Tables 1/1 of 2 ▸ 20240102", "events_YYYYMMDD")
	r.lacks("clicks")
	r.press("enter")
	r.contains("events_YYYYMMDD 1/1 of 4 ▸ 20240102")
	r.selected("acme-prod.events.events_20240102")
	r.lacks("events_20240101")
	r.press("esc")
	r.contains("Tables 1/1 of 2 ▸ 20240102")
	if s := r.m.sel(); s.node.Shard == nil || s.kind != "shard_family" {
		t.Fatalf("back on the family row: %+v", s)
	}
	r.press("esc")
	r.contains("Tables 2/2", "clicks")
}

func TestMillerFilterWithOneProjectOn(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("ctrl+x", "p", "o", "esc")
	r.lacks("Projects")
	r.press("/", "fin", "enter")
	r.contains("Datasets 1/1 of 4 ▸ fin")
	r.selected("acme-prod.finance")
}

func TestTreeFilterShowsMatchesWithTheirAncestors(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("L", "l")
	r.press("/", "ord")
	r.contains("filter tree ▸", "▾ acme-prod", "▾ sales", "order_items", "v_orders", "▾ us_archive", "orders_2019", "▾ acme-staging")
	r.lacks("events", "finance", "customers")
	r.selected("acme-prod.sales.orders")
	r.press("enter")
	r.contains("Explorer  4/10 ▸ ord · 5 matches")
	r.press("g", "g", "h")
	r.lacks("order_items")
	r.press("l", "j", "j", "j")
	r.selected("acme-prod.sales.orders")
	r.press("esc")
	r.contains("▾ acme-prod", "finance", "events", "▸ sales", "▸ acme-staging")
	r.selected("acme-prod.sales")
}

func TestTreeFilterContainerMatchedByNameOpensToEverything(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("L", "/", "fin", "enter")
	r.contains("▾ acme-prod", "▸ finance")
	r.lacks("refunds", "acme-staging")
	r.selected("acme-prod.finance")
	r.press("l")
	r.contains("▾ finance", "refunds", "invoices")
}

func TestJumpClearsTheFiltersThatHideTheTarget(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("l", "j", "j", "l", "/", "ord", "enter")
	r.press("ctrl+t", "refunds", "enter")
	r.selected("acme-prod.finance.refunds")
	r.lacks("▸ ord")
	r.press("/", "ref", "enter", "ctrl+t", "acme-prod.finance.refunds", "enter")
	r.contains("Tables 1/1 of 2 ▸ ref")

	r.press("L", "/", "orders_2019", "enter")
	r.press("ctrl+t", "invoices", "enter")
	r.selected("acme-prod.finance.invoices")
	r.lacks("orders_2019")
}

func TestEachExplorerStyleKeepsItsOwnFilter(t *testing.T) {
	r := newRig(t, 140, 30)
	r.press("l", "/", "s", "enter")
	r.press("L")
	r.selected("acme-prod.sales")
	if r.m.tree.f.active() {
		t.Fatal("the tree has a filter of its own")
	}
	r.press("/", "sal", "enter")
	r.contains("▸ sal · 2 matches")
	r.press("L", "L")
	r.selected("acme-prod.events.clicks")
	r.contains("Datasets 1/3 of 4 ▸ s")
	r.press("h", "j", "j")
	r.press("L")
	r.selected("acme-prod.us_archive")
	r.lacks("▸ sal · ")
}

func TestFiltersFollowCatalogChanges(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("L", "/", "orders", "enter")
	r.contains("▾ acme-staging")
	r.press("ctrl+x", "p", "j", "space", "esc")
	r.lacks("acme-staging")
	r.contains("▸ orders · 4 matches")
	r.selected("acme-prod.sales.orders")
}

func TestTypingAFilterNeverTriggersCommands(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("/", "qLPrRy?gg")
	if f := r.m.typingFilter(); f == nil || f.in.value() != "qLPrRy?gg" {
		t.Fatalf("printable keys must type: %+v", f)
	}
	if r.m.syncPhase != syncIdle || r.m.style != styleColumns || r.m.ov != ovNone {
		t.Fatal("no command may run while typing")
	}
}

func TestStaleFilterAnswersAreDropped(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("L", "/")
	f := &r.m.tree.f
	f.in.set("ord")
	first := r.m.refilter(f)
	f.in.set("refunds")
	second := r.m.refilter(f)
	r.send(second())
	r.send(first())
	if len(f.rank) != 1 {
		t.Fatalf("%v", f.rank)
	}
}

func TestPaletteFilterFromTheEditor(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("ctrl+w", "l")
	r.press("ctrl+p", "Filter", "enter")
	if r.m.zone != ZoneExplorer || r.m.typingFilter() == nil {
		t.Fatalf("zone %v, filter input open: %v", r.m.zone, r.m.typingFilter() != nil)
	}
}

func TestFilteringProjectsToOneKeepsTheProjectColumn(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("/", "staging", "enter")
	r.contains("Projects 1/1 of 2 ▸ sta")
	r.selected("acme-staging")
	r.press("esc")
	r.contains("Projects 2/2")
}

func TestClearingTheTreeFilterRestoresTheExpansionExactly(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("L", "l", "/", "ord", "enter")
	r.selected("acme-prod.sales.orders")
	r.press("esc")
	r.contains("▾ acme-prod", "▸ sales", "▸ acme-staging")
	r.lacks("order_items")
	r.selected("acme-prod.sales")
}

func TestEscLeavesTheSchemaBeforeClearingTheFilter(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("l", "j", "j", "l", "/", "ord", "enter", "enter", "ctrl+w", "z")
	if r.m.zone != ZoneSchema || r.m.maxed != ZoneSchema {
		t.Fatalf("enter goes to the Schema pane, ctrl+w z maximises it: %s %q", r.m.zone, r.m.maxed)
	}
	r.press("esc")
	if r.m.zone != ZoneExplorer || !r.m.mil.f[2].active() {
		t.Fatalf("first esc leaves the Schema pane only (pane %s)", r.m.zone)
	}
	r.press("esc")
	if r.m.mil.f[2].active() {
		t.Fatal("second esc clears the filter")
	}
}

func TestMatchCountsCountWhatTheListShows(t *testing.T) {
	r := newRig(t, 160, 30)
	r.press("l", "l", "/", "2024", "enter")
	r.contains("Tables 1/1 of 2 ▸ 2024")
	r.press("/")
	r.contains("1 match")
	r.press("esc", "esc", "L", "/", "type:view", "enter")
	r.contains("▸ type:view · 1 match")
}
