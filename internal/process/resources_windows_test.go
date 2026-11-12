//go:build windows

package process

import "testing"

func TestCurrentResourcesReportProcessLifetime(t *testing.T) {
	snapshot := CurrentResources()
	if snapshot.Scope != "client_process_lifetime" || snapshot.PeakWorkingSetBytes == nil || *snapshot.PeakWorkingSetBytes == 0 || snapshot.CPUTimeMS == nil || *snapshot.CPUTimeMS < 0 {
		t.Fatalf("missing current process lifetime metrics: %+v", snapshot)
	}
	next := CurrentResources()
	if next.PeakWorkingSetBytes == nil || next.CPUTimeMS == nil || *next.PeakWorkingSetBytes < *snapshot.PeakWorkingSetBytes || *next.CPUTimeMS < *snapshot.CPUTimeMS {
		t.Fatalf("lifetime counters moved backwards: first=%+v next=%+v", snapshot, next)
	}
}
