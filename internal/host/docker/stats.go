package docker

import (
	"time"

	"github.com/docker/docker/api/types/container"

	"github.com/camden-brown/garrison/internal/host"
)

// sampleFrom turns one frame of Docker's stats stream into a host.Sample.
//
// Both interesting numbers here are computed rather than read, and both are
// wrong in a way that looks plausible if you read them straight — see
// cpuPercent and memoryBytes.
func sampleFrom(s container.StatsResponse, at time.Time) host.Sample {
	rx, tx := networkTotals(s.Networks)
	return host.Sample{
		At:         at,
		CPUPct:     cpuPercent(s.CPUStats, s.PreCPUStats),
		MemBytes:   memoryBytes(s.MemoryStats),
		MemLimit:   int64(s.MemoryStats.Limit),
		NetRxBytes: rx,
		NetTxBytes: tx,
	}
}

// cpuPercent is (container delta / system delta) * cores * 100.
//
// The endpoint reports cumulative nanoseconds, so a raw reading is the CPU
// time since the container booted and means nothing. Docker gives us the
// previous frame in the same message, which is why no state is kept here.
func cpuPercent(cur, pre container.CPUStats) float64 {
	if pre.SystemUsage == 0 {
		// No previous frame. Docker zeroes PreCPUStats on the first message
		// of a stream, and measuring "since the container booted" against
		// "since the host booted" reports a busy container at 400% for its
		// first second. SystemUsage is host-wide CPU time, so a genuine
		// reading is never zero — this only matches the absent baseline.
		return 0
	}

	cpuDelta := delta(cur.CPUUsage.TotalUsage, pre.CPUUsage.TotalUsage)
	sysDelta := delta(cur.SystemUsage, pre.SystemUsage)
	if cpuDelta <= 0 || sysDelta <= 0 {
		// An idle container, or one whose counters went backwards across a
		// restart. Both are 0%, not a spike.
		return 0
	}

	cores := float64(cur.OnlineCPUs)
	if cores == 0 {
		cores = float64(len(cur.CPUUsage.PercpuUsage))
	}
	if cores == 0 {
		cores = 1
	}
	return (cpuDelta / sysDelta) * cores * 100
}

// memoryBytes is usage minus the page cache.
//
// Under Docker Desktop's WSL2 backend `usage` includes the page cache, which a
// file-writing game server fills to the limit within hours. Reporting it
// unadjusted makes every healthy server look like it is one allocation from an
// OOM kill, and the operator learns to ignore the memory column — which is the
// column that would have told them about the real one.
//
// cgroup v2 calls the deduction inactive_file; v1 called it
// total_inactive_file. Windows containers report neither and use the private
// working set instead.
func memoryBytes(m container.MemoryStats) int64 {
	if m.Usage == 0 && m.PrivateWorkingSet > 0 {
		return int64(m.PrivateWorkingSet)
	}

	cache := m.Stats["inactive_file"]
	if cache == 0 {
		cache = m.Stats["total_inactive_file"]
	}
	if cache > m.Usage {
		return 0
	}
	return int64(m.Usage - cache)
}

// networkTotals sums every interface. A container with a second network is
// rare and summing is the honest answer for a single number.
func networkTotals(nets map[string]container.NetworkStats) (rx, tx int64) {
	for _, n := range nets {
		rx += int64(n.RxBytes)
		tx += int64(n.TxBytes)
	}
	return rx, tx
}

// delta subtracts two cumulative counters without wrapping. Unsigned
// subtraction underflows into a number the size of the universe, which as a
// CPU percentage is a very memorable bug.
func delta(cur, pre uint64) float64 {
	if cur < pre {
		return 0
	}
	return float64(cur - pre)
}
