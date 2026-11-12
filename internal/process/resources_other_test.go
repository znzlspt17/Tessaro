//go:build !windows

package process

import "testing"

func TestCurrentResourcesUnavailableOnOtherPlatforms(t *testing.T) {
	snapshot := CurrentResources()
	if snapshot.Scope != "unavailable" || snapshot.PeakWorkingSetBytes != nil || snapshot.CPUTimeMS != nil {
		t.Fatalf("unsupported platform invented values: %+v", snapshot)
	}
}
