package perf

import (
	"regexp"
	"sort"
	"strings"
	"time"

	"ovc-agent/internal/protocol"
)

// Sample is one collection of every counter: Counters[key][instance] = value.
type Sample struct {
	At            time.Time
	Counters      map[string]map[string]float64
	MemTotalBytes float64 // 0 when unknown
	MemAvailBytes float64
}

// Source produces Samples; the PDH implementation lives in pdh_windows.go.
type Source interface {
	Sample() (*Sample, error)
	Close()
}

// acc accumulates one metric over a window: mean and peak.
type acc struct {
	sum, max float64
	n        int
}

func (a *acc) add(v float64) {
	if a.n == 0 || v > a.max {
		a.max = v
	}
	a.sum += v
	a.n++
}

// window aggregates the samples of one collection interval.
type window struct {
	start   time.Time
	samples int
	accs    map[string]*acc
	disks   map[string]bool            // volume instances
	nics    map[string]bool            // network interface instances
	vms     map[string]bool            // VM ids seen running
	vmDisks map[string]map[string]bool // VM id -> VHD paths
}

func newWindow(start time.Time) *window {
	return &window{
		start:   start,
		accs:    make(map[string]*acc),
		disks:   make(map[string]bool),
		nics:    make(map[string]bool),
		vms:     make(map[string]bool),
		vmDisks: make(map[string]map[string]bool),
	}
}

// k builds an accumulator key; NUL never appears in instance names or paths.
func k(parts ...string) string { return strings.Join(parts, "\x00") }

func (w *window) put(key string, v float64) {
	a := w.accs[key]
	if a == nil {
		a = &acc{}
		w.accs[key] = a
	}
	a.add(v)
}

func (w *window) mean(key string) *float64 {
	a := w.accs[key]
	if a == nil || a.n == 0 {
		return nil
	}
	v := a.sum / float64(a.n)
	return &v
}

func (w *window) peak(key string) *float64 {
	a := w.accs[key]
	if a == nil || a.n == 0 {
		return nil
	}
	v := a.max
	return &v
}

// hostDiskFields maps LogicalDisk counters to payload fields and unit conversions.
var hostDiskFields = []struct {
	counter, field string
	conv           func(float64) float64
}{
	{cDiskReadBps, "readBps", nil},
	{cDiskWriteBps, "writeBps", nil},
	{cDiskReadOps, "readIops", nil},
	{cDiskWriteOps, "writeIops", nil},
	{cDiskReadLat, "readLatencyMs", secToMs},
	{cDiskWriteLat, "writeLatencyMs", secToMs},
	{cDiskQueue, "queueLength", nil},
	{cDiskIdle, "busyPercent", func(idle float64) float64 { return clamp(100-idle, 0, 100) }},
}

var vmDiskFields = []struct{ counter, field string }{
	{cVSDReadBps, "readBps"},
	{cVSDWriteBps, "writeBps"},
	{cVSDReadOps, "readIops"},
	{cVSDWriteOps, "writeIops"},
	{cVSDLatency, "latencyMs"}, // AverageTimer: PDH formats it in seconds
	{cVSDQueue, "queueLength"},
}

func secToMs(v float64) float64 { return v * 1000 }

// unnamedVolume matches LogicalDisk instances of volumes without a drive letter
// or mount path.
var unnamedVolume = regexp.MustCompile(`^HarddiskVolume\d+$`)

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// add folds one sample into the window. It returns the VM counter instances
// ("<counter>:<instance>") that could not be mapped to a VM. A new one means the
// topology is probably stale (a VM was created, renamed or got a new disk);
// some never map (an ISO in a DVD drive, a host vNIC) - the sampler tells them
// apart.
func (w *window) add(s *Sample, topo *Topology) (unmatched map[string]bool) {
	w.samples++
	w.addHost(s)
	if topo == nil {
		topo = NewTopology(nil, nil, nil)
	}
	return w.addVMs(s, topo)
}

func (w *window) addHost(s *Sample) {
	if v, ok := hostCPU(s); ok {
		w.put("host.cpu", v)
	}
	if s.MemTotalBytes > 0 {
		used := s.MemTotalBytes - s.MemAvailBytes
		w.put("host.memTotal", s.MemTotalBytes)
		w.put("host.memUsed", used)
		w.put("host.memPct", used/s.MemTotalBytes*100)
	}

	for _, f := range hostDiskFields {
		for inst, v := range s.Counters[f.counter] {
			if f.conv != nil {
				v = f.conv(v)
			}
			if inst == totalInstance {
				w.put(k("host.disk", f.field), v)
				continue
			}
			if unnamedVolume.MatchString(inst) {
				continue // EFI / recovery partition: only in the total
			}
			w.disks[inst] = true
			w.put(k("disk", inst, f.field), v)
		}
	}
	total := s.Counters[cDiskReadLat][totalInstance]
	if wr := s.Counters[cDiskWriteLat][totalInstance]; wr > total {
		total = wr
	}
	if _, ok := s.Counters[cDiskReadLat][totalInstance]; ok {
		w.put("host.disk.latencyMax", secToMs(total))
	}

	for _, dir := range []struct{ counter, field string }{{cNetRx, "rx"}, {cNetTx, "tx"}} {
		values := s.Counters[dir.counter]
		if len(values) == 0 {
			continue
		}
		sum := 0.0
		for inst, v := range values {
			w.nics[inst] = true
			w.put(k("net", inst, dir.field), v)
			sum += v
		}
		w.put(k("host.net", dir.field), sum)
	}
}

// hostCPU prefers the hypervisor's view of the logical processors; the
// Processor sets only see the root partition on a Hyper-V host.
func hostCPU(s *Sample) (float64, bool) {
	for _, key := range []string{cHypervisorCPU, cProcUtility, cProcTime} {
		if v, ok := s.Counters[key][totalInstance]; ok {
			return clamp(v, 0, 100), true
		}
	}
	return 0, false
}

func (w *window) addVMs(s *Sample, topo *Topology) (unmatched map[string]bool) {
	unmatched = make(map[string]bool)
	// CPU: average of the VM's virtual processors.
	type cpuSum struct {
		sum float64
		n   int
	}
	cpu := make(map[string]*cpuSum)
	for inst, v := range s.Counters[cVPGuestRun] {
		name := vpInstanceVM(inst)
		if name == "" {
			continue
		}
		id, ok := topo.vmByName(name)
		if !ok {
			if _, known := topo.idsByName[strings.ToLower(name)]; !known {
				unmatched[cVPGuestRun+":"+name] = true
			}
			continue
		}
		c := cpu[id]
		if c == nil {
			c = &cpuSum{}
			cpu[id] = c
		}
		c.sum += v
		c.n++
	}
	for id, c := range cpu {
		w.vms[id] = true
		w.put(k("vm", id, "cpu"), clamp(c.sum/float64(c.n), 0, 100))
	}

	// Memory: Dynamic Memory counters, else the assigned memory from topology.
	withMem := make(map[string]bool)
	for inst, v := range s.Counters[cVMMemory] {
		if id, ok := topo.vmByName(inst); ok {
			w.vms[id] = true
			withMem[id] = true
			w.put(k("vm", id, "mem"), v*1024*1024)
		} else if _, known := topo.idsByName[strings.ToLower(inst)]; !known && inst != totalInstance {
			unmatched[cVMMemory+":"+inst] = true
		}
	}
	for inst, v := range s.Counters[cVMPressure] {
		if id, ok := topo.vmByName(inst); ok {
			w.put(k("vm", id, "pressure"), v)
		}
	}
	for id := range cpu {
		if vm, ok := topo.VM(id); ok && !withMem[id] && vm.MemoryAssigned > 0 {
			w.put(k("vm", id, "mem"), vm.MemoryAssigned)
		}
	}

	// Disks: per VHD, plus per-VM totals and an IOPS-weighted latency.
	type diskSum struct {
		readBps, writeBps, readIops, writeIops float64
		latWeighted, latPlain                  float64
		latN                                   int
		has                                    bool
	}
	disks := make(map[string]*diskSum)
	ds := func(id string) *diskSum {
		d := disks[id]
		if d == nil {
			d = &diskSum{}
			disks[id] = d
		}
		return d
	}
	for _, f := range vmDiskFields {
		for inst, v := range s.Counters[f.counter] {
			disk, ok := topo.diskFor(inst)
			if !ok {
				if f.counter == cVSDReadBps && inst != totalInstance {
					unmatched[cVSDReadBps+":"+inst] = true
				}
				continue
			}
			w.vms[disk.VMID] = true
			if w.vmDisks[disk.VMID] == nil {
				w.vmDisks[disk.VMID] = make(map[string]bool)
			}
			w.vmDisks[disk.VMID][disk.Path] = true
			if f.counter == cVSDLatency {
				v = secToMs(v)
			}
			w.put(k("vmdisk", disk.VMID, disk.Path, f.field), v)

			d := ds(disk.VMID)
			d.has = true
			switch f.counter {
			case cVSDReadBps:
				d.readBps += v
			case cVSDWriteBps:
				d.writeBps += v
			case cVSDReadOps:
				d.readIops += v
			case cVSDWriteOps:
				d.writeIops += v
			case cVSDLatency:
				iops := s.Counters[cVSDReadOps][inst] + s.Counters[cVSDWriteOps][inst]
				d.latWeighted += v * iops
				d.latPlain += v
				d.latN++
			}
		}
	}
	for id, d := range disks {
		if !d.has {
			continue
		}
		w.put(k("vm", id, "readBps"), d.readBps)
		w.put(k("vm", id, "writeBps"), d.writeBps)
		w.put(k("vm", id, "readIops"), d.readIops)
		w.put(k("vm", id, "writeIops"), d.writeIops)
		if d.latN > 0 {
			if iops := d.readIops + d.writeIops; iops > 0 {
				w.put(k("vm", id, "latency"), d.latWeighted/iops)
			} else {
				w.put(k("vm", id, "latency"), d.latPlain/float64(d.latN))
			}
		}
	}

	// Network: sum of the VM's adapters.
	for _, dir := range []struct{ counter, field string }{{cVNARx, "rx"}, {cVNATx, "tx"}} {
		sums := make(map[string]float64)
		for inst, v := range s.Counters[dir.counter] {
			if id, ok := topo.vmForNIC(inst); ok {
				sums[id] += v
			} else if dir.counter == cVNARx && inst != totalInstance {
				unmatched[cVNARx+":"+inst] = true
			}
		}
		for id, v := range sums {
			w.vms[id] = true
			w.put(k("vm", id, dir.field), v)
		}
	}
	return unmatched
}

// hostPayload builds the <hostid>.host_metrics message for this window.
func (w *window) hostPayload(reportedAt string, topo *Topology) protocol.HostMetricsPayload {
	p := protocol.HostMetricsPayload{
		ReportedAt:         reportedAt,
		CPUPercent:         w.mean("host.cpu"),
		CPUMaxPercent:      w.peak("host.cpu"),
		MemPercent:         w.mean("host.memPct"),
		MemUsedBytes:       w.mean("host.memUsed"),
		MemTotalBytes:      w.mean("host.memTotal"),
		DiskReadBps:        w.mean(k("host.disk", "readBps")),
		DiskWriteBps:       w.mean(k("host.disk", "writeBps")),
		DiskReadIops:       w.mean(k("host.disk", "readIops")),
		DiskWriteIops:      w.mean(k("host.disk", "writeIops")),
		DiskReadLatencyMs:  w.mean(k("host.disk", "readLatencyMs")),
		DiskWriteLatencyMs: w.mean(k("host.disk", "writeLatencyMs")),
		DiskLatencyMaxMs:   w.peak("host.disk.latencyMax"),
		NetRxBps:           w.mean(k("host.net", "rx")),
		NetTxBps:           w.mean(k("host.net", "tx")),
		Disks:              []protocol.HostDiskStats{},
		Net:                []protocol.HostNetStats{},
	}
	for _, name := range sortedKeys(w.disks) {
		p.Disks = append(p.Disks, protocol.HostDiskStats{
			Name:           name,
			Label:          topo.VolumeLabel(name),
			ReadBps:        w.mean(k("disk", name, "readBps")),
			WriteBps:       w.mean(k("disk", name, "writeBps")),
			ReadIops:       w.mean(k("disk", name, "readIops")),
			WriteIops:      w.mean(k("disk", name, "writeIops")),
			ReadLatencyMs:  w.mean(k("disk", name, "readLatencyMs")),
			WriteLatencyMs: w.mean(k("disk", name, "writeLatencyMs")),
			QueueLength:    w.mean(k("disk", name, "queueLength")),
			BusyPercent:    w.mean(k("disk", name, "busyPercent")),
		})
	}
	for _, name := range sortedKeys(w.nics) {
		p.Net = append(p.Net, protocol.HostNetStats{
			Name:  name,
			RxBps: w.mean(k("net", name, "rx")),
			TxBps: w.mean(k("net", name, "tx")),
		})
	}
	return p
}

// vmPayload builds the <hostid>.vm_metrics message for this window. Only VMs
// that had counters (i.e. were running) during the window are included.
func (w *window) vmPayload(reportedAt string, topo *Topology) protocol.VMMetricsPayload {
	p := protocol.VMMetricsPayload{ReportedAt: reportedAt, VMs: []protocol.VMMetricStats{}}
	for _, id := range sortedKeys(w.vms) {
		name := ""
		if topo != nil {
			if vm, ok := topo.VM(id); ok {
				name = vm.Name
			}
		}
		vm := protocol.VMMetricStats{
			ID:            id,
			Name:          name,
			CPUPercent:    w.mean(k("vm", id, "cpu")),
			CPUMaxPercent: w.peak(k("vm", id, "cpu")),
			MemBytes:      w.mean(k("vm", id, "mem")),
			MemPressure:   w.mean(k("vm", id, "pressure")),
			DiskReadBps:   w.mean(k("vm", id, "readBps")),
			DiskWriteBps:  w.mean(k("vm", id, "writeBps")),
			DiskReadIops:  w.mean(k("vm", id, "readIops")),
			DiskWriteIops: w.mean(k("vm", id, "writeIops")),
			DiskLatencyMs: w.mean(k("vm", id, "latency")),
			NetRxBps:      w.mean(k("vm", id, "rx")),
			NetTxBps:      w.mean(k("vm", id, "tx")),
			Disks:         []protocol.VMDiskStats{},
		}
		for _, path := range sortedKeys(w.vmDisks[id]) {
			vm.Disks = append(vm.Disks, protocol.VMDiskStats{
				Path:        path,
				ReadBps:     w.mean(k("vmdisk", id, path, "readBps")),
				WriteBps:    w.mean(k("vmdisk", id, path, "writeBps")),
				ReadIops:    w.mean(k("vmdisk", id, path, "readIops")),
				WriteIops:   w.mean(k("vmdisk", id, path, "writeIops")),
				LatencyMs:   w.mean(k("vmdisk", id, path, "latencyMs")),
				QueueLength: w.mean(k("vmdisk", id, path, "queueLength")),
			})
		}
		p.VMs = append(p.VMs, vm)
	}
	return p
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
