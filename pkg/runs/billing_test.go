package runs

import (
	"testing"

	"github.com/davidsevcik/bigquery-tui/pkg/api"
)

func TestMayBeBilledUnlessTheRunIsOnAReservation(t *testing.T) {
	if !MayBeBilled(api.RunInfo{}) {
		t.Fatal("an unknown pricing model counts as on-demand")
	}
	if !MayBeBilled(api.RunInfo{Estimate: &api.Estimate{}}) {
		t.Fatal("on-demand is billed")
	}
	if MayBeBilled(api.RunInfo{Estimate: &api.Estimate{Capacity: true, ReservationID: "r"}}) {
		t.Fatal("a reservation is not billed per query")
	}
}
