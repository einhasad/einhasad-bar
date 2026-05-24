// Package stats sums CPU% and resident memory across a process and all of its
// descendants, replacing the awk `ps -Ao pid=,ppid=` process-tree walk from the
// SwiftBar setup. A managed `make run` forks `go run`, which forks the compiled
// binary, so per-tree aggregation is what makes the numbers meaningful.
package stats

import (
	"github.com/shirou/gopsutil/v3/process"
)

// Tree holds aggregated resource usage for a process subtree.
type Tree struct {
	Procs int     // number of processes in the tree
	CPU   float64 // summed CPU percent (lifetime average, like `ps %cpu`)
	RSSMB float64 // summed resident set size in MiB
}

// Collect walks pid and its descendants and aggregates their usage. A zero Tree
// is returned if the root process no longer exists.
func Collect(pid int) Tree {
	root, err := process.NewProcess(int32(pid))
	if err != nil {
		return Tree{}
	}

	var t Tree
	seen := map[int32]bool{}
	var visit func(p *process.Process)
	visit = func(p *process.Process) {
		if seen[p.Pid] {
			return
		}
		seen[p.Pid] = true
		t.Procs++
		if cpu, err := p.CPUPercent(); err == nil {
			t.CPU += cpu
		}
		if mem, err := p.MemoryInfo(); err == nil && mem != nil {
			t.RSSMB += float64(mem.RSS) / (1024 * 1024)
		}
		children, err := p.Children()
		if err != nil {
			return
		}
		for _, c := range children {
			visit(c)
		}
	}
	visit(root)
	return t
}
