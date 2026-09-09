package docker

import (
	"testing"

	"github.com/docker/docker/api/types/container"
)

func TestCPUPercent(t *testing.T) {
	tests := []struct {
		name string
		cur  container.CPUStats
		pre  container.CPUStats
		want float64
	}{
		{
			name: "half of four cores",
			cur: container.CPUStats{
				CPUUsage:    container.CPUUsage{TotalUsage: 2_000},
				SystemUsage: 16_000,
				OnlineCPUs:  4,
			},
			pre: container.CPUStats{
				CPUUsage:    container.CPUUsage{TotalUsage: 1_000},
				SystemUsage: 8_000,
			},
			want: 50,
		},
		{
			name: "the first frame of a stream has no previous and is not a spike",
			cur: container.CPUStats{
				CPUUsage:    container.CPUUsage{TotalUsage: 9_999_999},
				SystemUsage: 9_999_999,
				OnlineCPUs:  4,
			},
			pre:  container.CPUStats{},
			want: 0,
		},
		{
			name: "counters going backwards after a restart read as zero, not as 10^19%",
			cur: container.CPUStats{
				CPUUsage:    container.CPUUsage{TotalUsage: 5},
				SystemUsage: 10,
				OnlineCPUs:  2,
			},
			pre: container.CPUStats{
				CPUUsage:    container.CPUUsage{TotalUsage: 1_000_000},
				SystemUsage: 2_000_000,
			},
			want: 0,
		},
		{
			name: "core count falls back to the per-cpu list when OnlineCPUs is absent",
			cur: container.CPUStats{
				CPUUsage:    container.CPUUsage{TotalUsage: 2_000, PercpuUsage: []uint64{1, 2}},
				SystemUsage: 16_000,
			},
			pre: container.CPUStats{
				CPUUsage:    container.CPUUsage{TotalUsage: 1_000},
				SystemUsage: 8_000,
			},
			want: 25,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cpuPercent(tt.cur, tt.pre); got != tt.want {
				t.Errorf("cpuPercent() = %v, want %v", got, tt.want)
			}
		})
	}
}

// The WSL2 page-cache problem, which is the reason this function exists: the
// raw usage figure counts cache a game server fills within hours, and
// reporting it makes every healthy server look one allocation from an OOM.
func TestMemoryBytesExcludesPageCache(t *testing.T) {
	tests := []struct {
		name string
		in   container.MemoryStats
		want int64
	}{
		{
			name: "cgroup v2 deducts inactive_file",
			in: container.MemoryStats{
				Usage: 8 << 30,
				Stats: map[string]uint64{"inactive_file": 5 << 30},
			},
			want: 3 << 30,
		},
		{
			name: "cgroup v1 deducts total_inactive_file",
			in: container.MemoryStats{
				Usage: 8 << 30,
				Stats: map[string]uint64{"total_inactive_file": 6 << 30},
			},
			want: 2 << 30,
		},
		{
			name: "no cache figure reports usage unchanged",
			in:   container.MemoryStats{Usage: 4 << 30},
			want: 4 << 30,
		},
		{
			name: "windows containers report the private working set",
			in:   container.MemoryStats{PrivateWorkingSet: 2 << 30},
			want: 2 << 30,
		},
		{
			name: "cache larger than usage clamps to zero rather than underflowing",
			in: container.MemoryStats{
				Usage: 1 << 20,
				Stats: map[string]uint64{"inactive_file": 4 << 30},
			},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := memoryBytes(tt.in); got != tt.want {
				t.Errorf("memoryBytes() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestNetworkTotalsSumsEveryInterface(t *testing.T) {
	rx, tx := networkTotals(map[string]container.NetworkStats{
		"eth0": {RxBytes: 100, TxBytes: 10},
		"eth1": {RxBytes: 200, TxBytes: 20},
	})
	if rx != 300 || tx != 30 {
		t.Errorf("networkTotals() = (%d, %d), want (300, 30)", rx, tx)
	}
}
