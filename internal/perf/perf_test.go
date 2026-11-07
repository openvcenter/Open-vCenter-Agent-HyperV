package perf

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

const (
	vmA     = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	vmB     = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	nicA    = "11111111-2222-3333-4444-555555555555"
	diskA   = `C:\ClusterStorage\Volume1\web-01\Virtual Hard Disks\web-01.vhdx`
	diskA2  = `D:\Hyper-V\web-01 (data)\data#1.vhdx`
	diskB   = `C:\ClusterStorage\Volume1\db-01\db-01.vhdx`
	diskDup = `E:\VMs\copy\db-01.vhdx`
)

func testTopology() *Topology {
	return NewTopology(
		[]TopoVM{
			{ID: strings.ToUpper(vmA), Name: "web-01", MemoryAssigned: 4 << 30},
			{ID: vmB, Name: "db-01", MemoryAssigned: 8 << 30},
		},
		[]TopoDisk{{VMID: vmA, Path: diskA}, {VMID: vmA, Path: diskA2}, {VMID: vmB, Path: diskB}},
		[]TopoNIC{{VMID: vmA, ID: "Microsoft:" + strings.ToUpper(vmA) + `\` + nicA}},
	)
}

func TestTopologyDiskInstances(t *testing.T) {
	topo := testTopology()
	cases := map[string]string{
		// full path as PDH formats it, with and without the \\?\ prefix
		`C:-ClusterStorage-Volume1-web-01-Virtual Hard Disks-web-01.vhdx`:     diskA,
		`--?-C:-ClusterStorage-Volume1-web-01-Virtual Hard Disks-web-01.vhdx`: diskA,
		// PDH replaces ( ) # in instance names
		`D:-Hyper-V-web-01 [data]-data_1.vhdx`: diskA2,
		// truncated instance: suffix match
		`Volume1-db-01-db-01.vhdx`: diskB,
	}
	for inst, want := range cases {
		d, ok := topo.diskFor(inst)
		if !ok || d.Path != want {
			t.Errorf("diskFor(%q) = %q, %v; want %q", inst, d.Path, ok, want)
		}
	}
	if _, ok := topo.diskFor(`F:-other-unknown.vhdx`); ok {
		t.Error("unknown disk should not match")
	}
}

func TestTopologyAmbiguousFileName(t *testing.T) {
	topo := NewTopology(nil, []TopoDisk{{VMID: vmA, Path: diskB}, {VMID: vmB, Path: diskDup}}, nil)
	if _, ok := topo.diskFor(`G:-moved-db-01.vhdx`); ok {
		t.Error("a file name shared by two disks must not resolve")
	}
}

func TestTopologyNamesAndNICs(t *testing.T) {
	topo := testTopology()
	if id, ok := topo.vmByName("WEB-01"); !ok || id != vmA {
		t.Errorf("vmByName = %q, %v", id, ok)
	}
	dup := NewTopology([]TopoVM{{ID: vmA, Name: "x"}, {ID: vmB, Name: "x"}}, nil, nil)
	if _, ok := dup.vmByName("x"); ok {
		t.Error("duplicate VM names must not resolve")
	}
	if got := vpInstanceVM("web-01:Hv VP 3"); got != "web-01" {
		t.Errorf("vpInstanceVM = %q", got)
	}
	if got := vpInstanceVM("_Total"); got != "" {
		t.Errorf("vpInstanceVM(_Total) = %q", got)
	}
	inst := "web-01_Network Adapter_" + strings.ToUpper(vmA) + "--" + strings.ToUpper(nicA)
	if id, ok := topo.vmForNIC(inst); !ok || id != vmA {
		t.Errorf("vmForNIC = %q, %v", id, ok)
	}
	if _, ok := topo.vmForNIC("vEthernet (External)"); ok {
		t.Error("host vNIC must not match a VM")
	}
}

func sample(cpu, diskRead, vpA0, vpA1 float64) *Sample {
	return &Sample{
		At:            time.Now(),
		MemTotalBytes: 64 << 30,
		MemAvailBytes: 16 << 30,
		Counters: map[string]map[string]float64{
			cHypervisorCPU: {"_Total": cpu},
			cProcTime:      {"_Total": 1},
			cDiskReadBps:   {"_Total": diskRead, "0 C:": diskRead},
			cDiskReadLat:   {"_Total": 0.002, "0 C:": 0.002},
			cDiskWriteLat:  {"_Total": 0.004, "0 C:": 0.004},
			cDiskIdle:      {"_Total": 80, "0 C:": 80},
			cNetRx:         {"nic1": 100, "nic2": 50},
			cVPGuestRun:    {"web-01:Hv VP 0": vpA0, "web-01:Hv VP 1": vpA1, "_Total": 99, "new-vm:Hv VP 0": 5},
			cVMMemory:      {"web-01": 2048},
			cVMPressure:    {"web-01": 70},
			cVSDReadBps: {
				`C:-ClusterStorage-Volume1-web-01-Virtual Hard Disks-web-01.vhdx`: 1000,
				`D:-Hyper-V-web-01 [data]-data_1.vhdx`:                            3000,
				`C:-ClusterStorage-Volume1-db-01-db-01.vhdx`:                      500,
			},
			cVSDReadOps: {
				`C:-ClusterStorage-Volume1-web-01-Virtual Hard Disks-web-01.vhdx`: 10,
				`D:-Hyper-V-web-01 [data]-data_1.vhdx`:                            30,
			},
			cVSDLatency: { // seconds
				`C:-ClusterStorage-Volume1-web-01-Virtual Hard Disks-web-01.vhdx`: 0.002,
				`D:-Hyper-V-web-01 [data]-data_1.vhdx`:                            0.006,
			},
			cVNARx: {"web-01_Network Adapter_" + vmA + "--" + nicA: 400},
		},
	}
}

func approx(t *testing.T, name string, got *float64, want float64) {
	t.Helper()
	if got == nil {
		t.Errorf("%s = nil, want %v", name, want)
		return
	}
	if math.Abs(*got-want) > 1e-9 {
		t.Errorf("%s = %v, want %v", name, *got, want)
	}
}

func TestWindowAggregation(t *testing.T) {
	topo := testTopology()
	w := newWindow(time.Now())
	if got := w.add(sample(10, 100, 20, 40), topo); len(got) != 1 || !got[cVPGuestRun+":new-vm"] {
		t.Errorf("unmatched = %v, want the unknown VM's processor", got)
	}
	w.add(sample(30, 300, 60, 80), topo)

	h := w.hostPayload("now")
	approx(t, "cpuPercent", h.CPUPercent, 20)
	approx(t, "cpuMaxPercent", h.CPUMaxPercent, 30)
	approx(t, "memPercent", h.MemPercent, 75)
	approx(t, "diskReadBps", h.DiskReadBps, 200)
	approx(t, "diskReadLatencyMs", h.DiskReadLatencyMs, 2)
	approx(t, "diskLatencyMaxMs", h.DiskLatencyMaxMs, 4)
	approx(t, "netRxBps", h.NetRxBps, 150)
	if h.DiskWriteBps != nil {
		t.Error("a counter that was never read must stay nil")
	}
	if len(h.Disks) != 1 || h.Disks[0].Name != "0 C:" {
		t.Fatalf("disks = %+v", h.Disks)
	}
	approx(t, "disk busyPercent", h.Disks[0].BusyPercent, 20)

	v := w.vmPayload("now", topo)
	if len(v.VMs) != 2 {
		t.Fatalf("vms = %+v", v.VMs)
	}
	web := v.VMs[0]
	if web.ID != vmA || web.Name != "web-01" {
		t.Fatalf("first vm = %s %s", web.ID, web.Name)
	}
	approx(t, "vm cpuPercent", web.CPUPercent, 50) // mean of 30 and 70
	approx(t, "vm cpuMaxPercent", web.CPUMaxPercent, 70)
	approx(t, "vm memBytes", web.MemBytes, 2048*1024*1024)
	approx(t, "vm memPressure", web.MemPressure, 70)
	approx(t, "vm diskReadBps", web.DiskReadBps, 4000)
	approx(t, "vm diskReadIops", web.DiskReadIops, 40)
	approx(t, "vm diskLatencyMs", web.DiskLatencyMs, 5) // (2*10 + 6*30) / 40
	approx(t, "vm netRxBps", web.NetRxBps, 400)
	if len(web.Disks) != 2 || web.Disks[0].Path != diskA {
		t.Fatalf("vm disks = %+v", web.Disks)
	}

	db := v.VMs[1]
	approx(t, "db diskReadBps", db.DiskReadBps, 500)
	if db.CPUPercent != nil {
		t.Error("db-01 had no CPU counters")
	}

	if _, err := json.Marshal(v); err != nil {
		t.Fatal(err)
	}
}

func TestHostCPUFallback(t *testing.T) {
	s := &Sample{Counters: map[string]map[string]float64{cProcUtility: {"_Total": 130}}}
	if v, ok := hostCPU(s); !ok || v != 100 {
		t.Errorf("hostCPU = %v, %v; want clamped 100", v, ok)
	}
}

func TestIntervalFile(t *testing.T) {
	dir := t.TempDir()
	if got := LoadInterval(dir); got != DefaultInterval {
		t.Errorf("missing file = %d", got)
	}
	if err := SaveInterval(dir, 60); err != nil {
		t.Fatal(err)
	}
	if got := LoadInterval(dir); got != 60 {
		t.Errorf("LoadInterval = %d", got)
	}
	if ValidInterval(1) != minInterval || ValidInterval(99999) != maxInterval || ValidInterval(0) != DefaultInterval {
		t.Error("ValidInterval bounds")
	}
}

func TestHasNew(t *testing.T) {
	ignored := map[string]bool{"vsd_read_bps:C:-ISOs-setup.iso": true}
	if hasNew(map[string]bool{"vsd_read_bps:C:-ISOs-setup.iso": true}, ignored) {
		t.Error("an instance already unmapped after the refresh is not new")
	}
	if !hasNew(map[string]bool{"vp_guest_run:new-vm": true}, ignored) {
		t.Error("a new unmapped instance must mark the topology stale")
	}
}
