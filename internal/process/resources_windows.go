//go:build windows

package process

import (
	"golang.org/x/sys/windows"
	"unsafe"
)

type ResourceSnapshot struct {
	PeakWorkingSetBytes *uint64
	CPUTimeMS           *float64
	Scope               string
}

type processMemoryCounters struct {
	Size                       uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

// 2026-11-12: 프로세스 전체 수명 고점과 누적 CPU를 사용해 단일 trial의 독립 측정치로 오인하지 않게 한다.
func CurrentResources() ResourceSnapshot {
	out := ResourceSnapshot{Scope: "client_process_lifetime"}
	handle, err := windows.GetCurrentProcess()
	if err != nil {
		return out
	}
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("K32GetProcessMemoryInfo")
	if proc.Find() == nil {
		var counters processMemoryCounters
		counters.Size = uint32(unsafe.Sizeof(counters))
		success, _, _ := proc.Call(uintptr(handle), uintptr(unsafe.Pointer(&counters)), uintptr(counters.Size))
		if success != 0 {
			peak := uint64(counters.PeakWorkingSetSize)
			out.PeakWorkingSetBytes = &peak
		}
	}
	var created, exited, kernel, user windows.Filetime
	if windows.GetProcessTimes(handle, &created, &exited, &kernel, &user) == nil {
		ticks := func(ft windows.Filetime) uint64 { return uint64(ft.HighDateTime)<<32 | uint64(ft.LowDateTime) }
		ms := (float64(ticks(kernel)) + float64(ticks(user))) / 10000
		out.CPUTimeMS = &ms
	}
	return out
}
