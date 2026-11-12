//go:build !windows

package process

type ResourceSnapshot struct {
	PeakWorkingSetBytes *uint64
	CPUTimeMS           *float64
	Scope               string
}

func CurrentResources() ResourceSnapshot { return ResourceSnapshot{Scope: "unavailable"} }
