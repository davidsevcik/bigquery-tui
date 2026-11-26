package runs

import "github.com/davidsevcik/bigquery-tui/pkg/api"

// ADR-0011
func MayBeBilled(r api.RunInfo) bool { return r.Estimate == nil || !r.Estimate.Capacity }
