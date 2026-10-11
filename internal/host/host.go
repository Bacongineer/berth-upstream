// Package host reads the machine's CPU, memory and load from /proc. It is
// what a node knows about the box its GPUs sit in; off Linux it reports
// Supported=false rather than zeros that look like an idle machine.
package host

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"sync"
)

type Stats struct {
	Supported bool       `json:"supported"`
	CPU       float64    `json:"cpu_pct"` // all cores, since the previous Read
	Cores     int        `json:"cores"`
	RAMTotal  uint64     `json:"ram_total"`
	RAMUsed   uint64     `json:"ram_used"` // total minus available
	SwapTotal uint64     `json:"swap_total"`
	SwapUsed  uint64     `json:"swap_used"`
	Load      [3]float64 `json:"load"`
}

// Reader keeps the previous CPU counters so each Read returns usage over
// the interval since the last one.
type Reader struct {
	mu        sync.Mutex
	idle, tot uint64
	ProcRoot  string // default /proc; tests point it elsewhere
}

func (r *Reader) root() string {
	if r.ProcRoot == "" {
		return "/proc"
	}
	return r.ProcRoot
}

func (r *Reader) Read() Stats {
	var s Stats
	f, err := os.Open(r.root() + "/stat")
	if err != nil {
		return s
	}
	defer f.Close()
	s.Supported = true
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "cpu ") {
			var vals []uint64
			for _, x := range strings.Fields(line)[1:] {
				v, _ := strconv.ParseUint(x, 10, 64)
				vals = append(vals, v)
			}
			var tot uint64
			for _, v := range vals {
				tot += v
			}
			idle := vals[3]
			if len(vals) > 4 {
				idle += vals[4] // iowait
			}
			r.mu.Lock()
			if r.tot > 0 && tot > r.tot {
				s.CPU = 100 * (1 - float64(idle-r.idle)/float64(tot-r.tot))
			}
			r.idle, r.tot = idle, tot
			r.mu.Unlock()
		} else if strings.HasPrefix(line, "cpu") {
			s.Cores++
		}
	}
	mem := map[string]uint64{}
	if b, err := os.ReadFile(r.root() + "/meminfo"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			k, v, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			n, _ := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
			mem[k] = n * 1024
		}
	}
	s.RAMTotal = mem["MemTotal"]
	if a := mem["MemAvailable"]; a <= s.RAMTotal {
		s.RAMUsed = s.RAMTotal - a
	}
	s.SwapTotal = mem["SwapTotal"]
	if f := mem["SwapFree"]; f <= s.SwapTotal {
		s.SwapUsed = s.SwapTotal - f
	}
	if b, err := os.ReadFile(r.root() + "/loadavg"); err == nil {
		f := strings.Fields(string(b))
		for i := 0; i < 3 && i < len(f); i++ {
			s.Load[i], _ = strconv.ParseFloat(f[i], 64)
		}
	}
	return s
}
