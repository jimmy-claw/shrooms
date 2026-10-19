package hostload

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type text struct{ strings.Builder }

func (t *text) Family(name, typ, help string) { fmt.Fprintf(t, "# TYPE %s %s\n", name, typ) }
func (t *text) Sample(name string, v float64, labels ...string) {
	var l []string
	for i := 0; i+1 < len(labels); i += 2 {
		l = append(l, labels[i]+`="`+labels[i+1]+`"`)
	}
	if len(l) > 0 {
		name += "{" + strings.Join(l, ",") + "}"
	}
	fmt.Fprintf(t, "%s %g\n", name, v)
}

func TestTheMachinesLoad(t *testing.T) {
	d := t.TempDir()
	stat := filepath.Join(d, "stat")
	os.WriteFile(stat, []byte("cpu  1000 20 300 50000 40 0 6 0 0 0\ncpu0 500 10 150 25000 20 0 3 0 0 0\ncpu1 500 10 150 25000 20 0 3 0 0 0\n"), 0o600)
	load := filepath.Join(d, "loadavg")
	os.WriteFile(load, []byte("1.50 0.75 0.25 2/300 4242\n"), 0o600)
	mem := filepath.Join(d, "meminfo")
	os.WriteFile(mem, []byte("MemTotal:       16000000 kB\nMemAvailable:    8000000 kB\n"), 0o600)
	var out text
	Write(&out, Paths{Stat: stat, Loadavg: load, Meminfo: mem, Root: d})
	for _, want := range []string{`shrooms_host_cpu_seconds_total{mode="user"} 10`, `shrooms_host_cpu_seconds_total{mode="idle"} 500`,
		`shrooms_host_cpus 2`, `shrooms_host_load{window="5m"} 0.75`, `shrooms_host_memory_bytes{kind="total"} 1.6384e+10`,
		`shrooms_host_disk_bytes{kind="free"}`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("no %s in:\n%s", want, out.String())
		}
	}
}
