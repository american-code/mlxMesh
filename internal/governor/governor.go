// Package governor enforces contribution caps from proposal §9.3:
// a declared percentage of memory, checked against ACTUAL available headroom
// (never assumed from total RAM), and contribution active only while foregrounded.
//
// macOS aggressively reclaims memory — this package exists so capability.go
// never over-promises based on nameplate RAM.
package governor

import (
	"fmt"
	"runtime"

	"github.com/shirou/gopsutil/v3/mem"
)

// AvailableMemoryGB returns real-time available memory.
// On macOS this is mem.VirtualMemory().Available, which reflects
// memory genuinely free to use (excluding wired/kernel pages).
func AvailableMemoryGB() (float64, error) {
	v, err := mem.VirtualMemory()
	if err != nil {
		return 0, fmt.Errorf("read virtual memory: %w", err)
	}
	return float64(v.Available) / (1 << 30), nil
}

// TotalRAMGB returns the device's total installed RAM.
func TotalRAMGB() (float64, error) {
	v, err := mem.VirtualMemory()
	if err != nil {
		return 0, fmt.Errorf("read virtual memory: %w", err)
	}
	return float64(v.Total) / (1 << 30), nil
}

// EnforceContributionCap returns the memory ceiling (GB) this node may commit
// to inference right now: min(declaredCapPct * totalRAM, availableRAM).
// Called before accepting any job, not just at registration time.
func EnforceContributionCap(declaredCapPct float64) (float64, error) {
	if declaredCapPct <= 0 || declaredCapPct > 1 {
		return 0, fmt.Errorf("declaredCapPct must be in (0, 1], got %.2f", declaredCapPct)
	}
	total, err := TotalRAMGB()
	if err != nil {
		return 0, err
	}
	available, err := AvailableMemoryGB()
	if err != nil {
		return 0, err
	}
	cap := declaredCapPct * total
	if available < cap {
		return available, nil
	}
	return cap, nil
}

// IsForegrounded returns true when the node agent should accept jobs.
// Platform-specific implementations live in foreground_unix.go / foreground_other.go.

// SystemInfo returns a diagnostic snapshot used by `oim node status`.
func SystemInfo() (map[string]any, error) {
	v, err := mem.VirtualMemory()
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"platform":         runtime.GOOS + "/" + runtime.GOARCH,
		"is_apple_silicon": runtime.GOOS == "darwin" && runtime.GOARCH == "arm64",
		"total_ram_gb":     round2(float64(v.Total) / (1 << 30)),
		"available_ram_gb": round2(float64(v.Available) / (1 << 30)),
		"used_pct":         round2(v.UsedPercent),
	}, nil
}

func round2(f float64) float64 {
	return float64(int(f*100)) / 100
}
