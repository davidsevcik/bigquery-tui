package tui

import "testing"

func (r *rig) selected(want string) {
	r.t.Helper()
	if got := r.m.sel().path.String(); got != want {
		r.t.Fatalf("selection is %q, want %q\n%s", got, want, r.screen())
	}
}

func TestMillerFilterNarrowsTheFocusedColumn(t *testing.T) {
	r := newRig(t, 140, 30)     // an Explorer wide enough for two columns
	r.press("l", "j", "j", "l") // the tables of acme-prod.sales
	r.press("/", "ord")
	r.contains("filter tables ▸", "order_items", "v_orders")
	r.lacks("customers")
	// orders sorts after order_items but is the better match
	r.selected("acme-prod.sales.orders")
	// the other columns are left alone
	r.contains("finance", "us_archive")
	r.press("enter")
	r.lacks("filter tables ▸")
	r.contains("Tables 2/3 of 4 ▸ ord")
	r.press("k")
	r.selected("acme-prod.sales.order_items")
}

func TestMillerColumnFiltersAndTheLeftRule(t *testing.T) {
	r := newRig(t, 140, 30)             // an Explorer wide enough for two columns
	r.press("l", "/", "s", "enter")     // datasets with an s: events, sales, us_archive
	r.contains("Datasets 2/3 of 4 ▸ s") // on sales, the prefix match
	r.lacks("finance")
	r.press("l", "/", "ord", "enter") // the tables of sales
	r.contains("Datasets 2/3 of 4 ▸ s", "Tables 2/3 of 4 ▸ ord")
	// esc clears only the focused column
	r.press("esc")
	r.contains("Datasets 2/3 of 4 ▸ s", "Tables 3/4")
	r.selected("acme-prod.sales.orders")
	r.press("/", "zzz")
	r.contains("no match for “zzz”")
	r.press("enter")
	// moving left drops the filter of the column left behind and keeps the one landed on
	r.press("h")
	r.contains("Datasets 2/3 of 4 ▸ s")
	r.lacks("zzz")
	r.press("l")
	r.contains("Tables 1/4")
	// in single steps up to the projects, every deeper filter goes
	r.press("h", "h")
	r.lacks("▸ s")
}

func TestMillerFilterKeepsShardFamiliesByTheirMembers(t *testing.T) {
	r := newRig(t, 160, 30)
	r.press("ctrl+p", "Layout: focus", "enter") // the Explorer over the whole width
	r.press("l", "l", "/", "20240102", "enter") // the tables of acme-prod.events
	r.contains("Tables 1/1 of 2 ▸ 20240102", "events_YYYYMMDD")
	r.lacks("clicks")
	r.press("enter") // open the family: its members are narrowed too
	r.contains("events_YYYYMMDD 1/1 of 4 ▸ 20240102")
	r.selected("acme-prod.events.events_20240102")
	r.lacks("events_20240101")
	r.press("esc") // collapses the family first
	r.contains("Tables 1/1 of 2 ▸ 20240102")
	if s := r.m.sel(); s.node.Shard == nil || s.kind != "shard_family" {
		t.Fatalf("back on the family row: %+v", s)
	}
	r.press("esc") // then clears the filter
	r.contains("Tables 2/2", "clicks")
}

func TestMillerFilterWithOneProjectOn(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("ctrl+x", "p", "o", "esc") // only acme-prod on: the Miller view starts at the datasets
	r.lacks("Projects")
	r.press("/", "fin", "enter")
	r.contains("Datasets 1/1 of 4 ▸ fin")
	r.selected("acme-prod.finance")
}

func TestTreeFilterShowsMatchesWithTheirAncestors(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("L", "l") // the tree, with acme-prod opened by hand
	r.press("/", "ord")
	r.contains("filter tree ▸", "▾ acme-prod", "▾ sales", "order_items", "v_orders", "▾ us_archive", "orders_2019", "▾ acme-staging")
	r.lacks("events", "finance", "customers")
	r.selected("acme-prod.sales.orders") // exact match; the shorter path wins the tie
	r.press("enter")
	r.contains("Explorer  4/10 ▸ ord · 5 matches")
	// rows can still be collapsed while filtering
	r.press("g", "g", "h")
	r.lacks("order_items")
	r.press("l", "j", "j", "j") // reopen, back down to orders
	r.selected("acme-prod.sales.orders")
	// clearing restores the hand-made expansion; the cursor goes to the
	// nearest row still shown
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
	r.press("l") // a container found by its own name lists all its children
	r.contains("▾ finance", "refunds", "invoices")
}

func TestJumpClearsTheFiltersThatHideTheTarget(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("l", "j", "j", "l", "/", "ord", "enter") // Miller: the tables of sales, filtered
	r.press("ctrl+t", "refunds", "enter")
	r.selected("acme-prod.finance.refunds")
	r.lacks("▸ ord")
	// a target the filter shows keeps it
	r.press("/", "ref", "enter", "ctrl+t", "acme-prod.finance.refunds", "enter")
	r.contains("Tables 1/1 of 2 ▸ ref")

	r.press("L", "/", "orders_2019", "enter") // tree
	r.press("ctrl+t", "invoices", "enter")
	r.selected("acme-prod.finance.invoices")
	r.lacks("orders_2019")
}

func TestEachExplorerStyleKeepsItsOwnFilter(t *testing.T) {
	r := newRig(t, 140, 30)         // an Explorer wide enough for two columns
	r.press("l", "/", "s", "enter") // Miller datasets: on sales
	r.press("L")                    // tree, unfiltered, on sales too
	r.selected("acme-prod.sales")
	if r.m.tree.f.active() {
		t.Fatal("the tree has a filter of its own")
	}
	r.press("/", "sal", "enter")
	r.contains("▸ sal · 2 matches")
	// flat (which lists tables only, so it lands on the first one), then back
	// to Miller: its filter shows that table's dataset, so it stays
	r.press("L", "L")
	r.selected("acme-prod.events.clicks")
	r.contains("Datasets 1/3 of 4 ▸ s")
	r.press("h", "j", "j") // us_archive, which the tree filter hides
	r.press("L")
	r.selected("acme-prod.us_archive")
	r.lacks("▸ sal · ")
}

func TestFiltersFollowCatalogChanges(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("L", "/", "orders", "enter")
	r.contains("▾ acme-staging")
	r.press("ctrl+x", "p", "j", "space", "esc") // turn acme-staging off
	r.lacks("acme-staging")
	r.contains("▸ orders · 4 matches") // orders, order_items (fuzzy), v_orders, orders_2019
	r.selected("acme-prod.sales.orders")
}

func TestTypingAFilterNeverTriggersCommands(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("/", "qLPrRy?gg")
	if f := r.m.typingFilter(); f == nil || f.in.value() != "qLPrRy?gg" {
		t.Fatalf("printable keys must type: %+v", f)
	}
	if r.m.syncing || r.m.style != styleColumns || r.m.ov != ovNone {
		t.Fatal("no command may run while typing")
	}
}

func TestStaleFilterAnswersAreDropped(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("L", "/")
	f := &r.m.tree.f
	f.in.set("ord")
	first := r.m.refilter(f) // not delivered yet
	f.in.set("refunds")
	second := r.m.refilter(f)
	r.send(second())
	r.send(first()) // the older answer arrives late and must be ignored
	if len(f.rank) != 1 {
		t.Fatalf("%v", f.rank)
	}
}

func TestPaletteFilterFromTheEditor(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("ctrl+w", "l") // the Editor
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
	r.press("L", "l", "/", "ord", "enter") // acme-prod opened by hand, sales by the filter
	r.selected("acme-prod.sales.orders")
	r.press("esc")
	r.contains("▾ acme-prod", "▸ sales", "▸ acme-staging")
	r.lacks("order_items")
	r.selected("acme-prod.sales") // the nearest row still shown
}

func TestEscLeavesTheZoomBeforeClearingTheFilter(t *testing.T) {
	r := newRig(t, 120, 30)
	r.press("l", "j", "j", "l", "/", "ord", "enter", "z")
	if r.m.zone != ZoneSchema || r.m.maxed != ZoneSchema {
		t.Fatalf("z maximises the Schema pane: %s %q", r.m.zone, r.m.maxed)
	}
	r.press("esc")
	if r.m.zone != ZoneSchema || r.m.maxed != "" || !r.m.mil.f[2].active() {
		t.Fatalf("first esc only restores the Layout (pane %s, maxed %q)", r.m.zone, r.m.maxed)
	}
	r.press("esc")
	if r.m.zone != ZoneExplorer || !r.m.mil.f[2].active() {
		t.Fatalf("second esc leaves the Schema pane only (pane %s)", r.m.zone)
	}
	r.press("esc")
	if r.m.mil.f[2].active() {
		t.Fatal("third esc clears the filter")
	}
}

func TestMatchCountsCountWhatTheListShows(t *testing.T) {
	r := newRig(t, 160, 30)
	r.press("l", "l", "/", "2024", "enter") // events: the family, not its members
	r.contains("Tables 1/1 of 2 ▸ 2024")
	r.press("/")
	r.contains("1 match")
	r.press("esc", "esc", "L", "/", "type:view", "enter")
	r.contains("▸ type:view · 1 match") // v_orders, not the dataset and project holding it
}
