package gpu

import (
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	s, err := Parse(0, "0, NVIDIA GeForce RTX 4090, 24564, 23134, 1030\n",
		"9947, /opt/ninfer/bin/ninfer-serve, 23106\n12, odd, name, [N/A]\n", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Index != 0 || s.Name != "NVIDIA GeForce RTX 4090" {
		t.Fatalf("device = %d %q", s.Index, s.Name)
	}
	if s.Total != 24564*MiB || s.Used != 23134*MiB || s.Free != 1030*MiB {
		t.Fatalf("memory = %d/%d/%d", s.Total, s.Used, s.Free)
	}
	if len(s.Procs) != 2 || s.Procs[0].PID != 9947 || s.Procs[0].Used != 23106*MiB {
		t.Fatalf("procs = %+v", s.Procs)
	}
	if s.Procs[1].Name != "odd, name" || s.Procs[1].Used != 0 {
		t.Fatalf("comma name / N/A = %+v", s.Procs[1])
	}
	if s.Baseline() != 28*MiB {
		t.Fatalf("baseline = %d MiB", s.Baseline()/MiB)
	}
}

func TestParseNoApps(t *testing.T) {
	s, err := Parse(1, "1, X, 24564, 500, 24064", "", time.Time{})
	if err != nil || len(s.Procs) != 0 || s.Baseline() != 500*MiB {
		t.Fatalf("s=%+v err=%v", s, err)
	}
}

// A 5080 + 4070 Ti box: both cards inventoried with their own limits,
// the snapshot is the scheduled one, and a missing index is an error.
func TestParseTwoCards(t *testing.T) {
	mem := "0, NVIDIA GeForce RTX 5080, 16303, 9000, 7303\n1, NVIDIA GeForce RTX 4070 Ti, 12282, 400, 11882\n"
	s, err := Parse(1, mem, "", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Devices) != 2 || s.Devices[0].Total != 16303*MiB || s.Devices[1].Total != 12282*MiB {
		t.Fatalf("devices = %+v", s.Devices)
	}
	if s.Index != 1 || s.Name != "NVIDIA GeForce RTX 4070 Ti" || s.Total != 12282*MiB {
		t.Fatalf("scheduled = %d %q %d", s.Index, s.Name, s.Total)
	}
	if _, err := Parse(2, mem, "", time.Time{}); err == nil {
		t.Fatal("index 2 does not exist and must be an error")
	}
}

func TestParseTelemetry(t *testing.T) {
	devs, err := ParseDevices("0, NVIDIA GeForce RTX 4090, 24564, 23134, 1030, 97, 71, 402.55, 450.00, [N/A], 2730, 10501, 4, 16\n")
	if err != nil {
		t.Fatal(err)
	}
	d := devs[0]
	if d.Util != 97 || d.Temp != 71 || d.Power != 402.55 || d.PowerLimit != 450 || d.Fan != -1 || d.ClockSM != 2730 || d.PCIeGen != 4 || d.PCIeWidth != 16 {
		t.Fatalf("telemetry = %+v (an [N/A] fan must read -1)", d)
	}
}
