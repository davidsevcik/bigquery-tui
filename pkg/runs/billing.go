package runs

import "github.com/davidsevcik/bigquery-tui/pkg/api"

// MayBeBilled reports whether a Run may be billed for the bytes it
// processes, so that cancelling it may not stop the charge: it is on-demand
// by its own dry-run, or of unknown pricing, which counts as on-demand
// (spec 10.2). Pricing is per Run: the Execution project's pricing model
// (Follower.Pricing) does not decide it.
func MayBeBilled(r api.RunInfo) bool { return r.Estimate == nil || !r.Estimate.Capacity }
