package hostload

// How loaded the machine is: its CPUs, load, memory and disk (docs/metrics.md).
// Which machines carry the agents' work and which sit idle is a question of
// numbers first: the laptop goes offline, jimmy-crib is not the strongest, and
// atlas may be under-used (2026-10-16).
//
// From /proc and statfs, as a node exporter would, for the few series that
// answer that. Both the daemon and the agent export them, so a machine has
// them whichever of the two it runs, and the dashboard takes either.

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// Paths are where the load is read from; a test's fixtures.
type Paths struct{ Stat, Loadavg, Meminfo, Root string }

// Real is this machine's.
var Real = Paths{Stat: "/proc/stat", Loadavg: "/proc/loadavg", Meminfo: "/proc/meminfo", Root: "/"}

// Writer is what writes Prometheus' text format: a family's header, and a
// sample with its labels as name, value pairs.
type Writer interface {
	Family(name, typ, help string)
	Sample(name string, v float64, labels ...string)
}

// cpuModes are /proc/stat's cpu columns, in order, as node exporter names them.
var cpuModes = []string{"user", "nice", "system", "idle", "iowait", "irq", "softirq", "steal"}

// Write writes the machine's load.
func Write(e Writer, p Paths) {
	// CPU time by mode, all CPUs together, and how many there are.
	if f, err := os.Open(p.Stat); err == nil {
		sc := bufio.NewScanner(f)
		cpus := 0
		var total []float64
		for sc.Scan() {
			fs := strings.Fields(sc.Text())
			if len(fs) == 0 {
				continue
			}
			switch {
			case fs[0] == "cpu":
				for i := 1; i < len(fs) && i <= len(cpuModes); i++ {
					v, _ := strconv.ParseFloat(fs[i], 64)
					total = append(total, v/100) // USER_HZ is 100 on Linux
				}
			case strings.HasPrefix(fs[0], "cpu"):
				cpus++
			}
		}
		f.Close()
		if len(total) > 0 {
			e.Family("shrooms_host_cpu_seconds_total", "counter", "CPU time of all this machine's CPUs together, by mode (/proc/stat).")
			for i, v := range total {
				e.Sample("shrooms_host_cpu_seconds_total", v, "mode", cpuModes[i])
			}
		}
		if cpus > 0 {
			e.Family("shrooms_host_cpus", "gauge", "This machine's CPUs.")
			e.Sample("shrooms_host_cpus", float64(cpus))
		}
	}
	if b, err := os.ReadFile(p.Loadavg); err == nil {
		if fs := strings.Fields(string(b)); len(fs) >= 3 {
			e.Family("shrooms_host_load", "gauge", "This machine's load average over 1, 5 and 15 minutes.")
			for i, w := range []string{"1m", "5m", "15m"} {
				v, _ := strconv.ParseFloat(fs[i], 64)
				e.Sample("shrooms_host_load", v, "window", w)
			}
		}
	}
	if f, err := os.Open(p.Meminfo); err == nil {
		mem := map[string]float64{}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fs := strings.Fields(sc.Text())
			if len(fs) >= 2 {
				v, _ := strconv.ParseFloat(fs[1], 64)
				mem[strings.TrimSuffix(fs[0], ":")] = v * 1024 // kB
			}
		}
		f.Close()
		if mem["MemTotal"] > 0 {
			e.Family("shrooms_host_memory_bytes", "gauge", "This machine's memory: total, and available to programs (/proc/meminfo).")
			e.Sample("shrooms_host_memory_bytes", mem["MemTotal"], "kind", "total")
			e.Sample("shrooms_host_memory_bytes", mem["MemAvailable"], "kind", "available")
		}
	}
	var st syscall.Statfs_t
	if syscall.Statfs(p.Root, &st) == nil && st.Blocks > 0 {
		e.Family("shrooms_host_disk_bytes", "gauge", "The root filesystem: its size, and what is free.")
		bs := float64(st.Bsize)
		e.Sample("shrooms_host_disk_bytes", float64(st.Blocks)*bs, "kind", "size")
		e.Sample("shrooms_host_disk_bytes", float64(st.Bavail)*bs, "kind", "free")
	}
}
