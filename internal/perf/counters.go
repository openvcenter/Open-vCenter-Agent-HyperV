// Package perf samples host and VM performance counters (PDH) inside the agent
// service and publishes window aggregates to the <hostid>.host_metrics /
// <hostid>.vm_metrics queues. The hot path never starts PowerShell: counters are
// read through pdh.dll every SampleInterval, and only the VM topology (which
// counter instance belongs to which VM) is refreshed with a slow PowerShell call.
package perf

// Counter keys used in Sample.Counters. Each maps to one English PDH path
// (PdhAddEnglishCounterW, so non-English Windows works too).
const (
	cHypervisorCPU = "hv_cpu"
	cProcUtility   = "proc_utility"
	cProcTime      = "proc_time"

	cDiskReadBps  = "disk_read_bps"
	cDiskWriteBps = "disk_write_bps"
	cDiskReadOps  = "disk_read_ops"
	cDiskWriteOps = "disk_write_ops"
	cDiskReadLat  = "disk_read_lat"
	cDiskWriteLat = "disk_write_lat"
	cDiskQueue    = "disk_queue"
	cDiskIdle     = "disk_idle"

	cNetRx = "net_rx"
	cNetTx = "net_tx"

	cVPGuestRun  = "vp_guest_run"
	cVMMemory    = "vm_memory"
	cVMPressure  = "vm_pressure"
	cVSDReadBps  = "vsd_read_bps"
	cVSDWriteBps = "vsd_write_bps"
	cVSDReadOps  = "vsd_read_ops"
	cVSDWriteOps = "vsd_write_ops"
	cVSDLatency  = "vsd_latency"
	cVSDQueue    = "vsd_queue"
	cVNARx       = "vna_rx"
	cVNATx       = "vna_tx"
)

// counterPaths lists every counter the source adds to its PDH query. Counters
// whose set does not exist on the host are skipped.
var counterPaths = []struct{ key, path string }{
	// On the Hyper-V root, \Processor only sees the root partition; the
	// hypervisor's logical processors give the real host CPU.
	{cHypervisorCPU, `\Hyper-V Hypervisor Logical Processor(_Total)\% Total Run Time`},
	{cProcUtility, `\Processor Information(_Total)\% Processor Utility`},
	{cProcTime, `\Processor(_Total)\% Processor Time`},

	{cDiskReadBps, `\PhysicalDisk(*)\Disk Read Bytes/sec`},
	{cDiskWriteBps, `\PhysicalDisk(*)\Disk Write Bytes/sec`},
	{cDiskReadOps, `\PhysicalDisk(*)\Disk Reads/sec`},
	{cDiskWriteOps, `\PhysicalDisk(*)\Disk Writes/sec`},
	{cDiskReadLat, `\PhysicalDisk(*)\Avg. Disk sec/Read`},
	{cDiskWriteLat, `\PhysicalDisk(*)\Avg. Disk sec/Write`},
	{cDiskQueue, `\PhysicalDisk(*)\Current Disk Queue Length`},
	{cDiskIdle, `\PhysicalDisk(*)\% Idle Time`},

	{cNetRx, `\Network Interface(*)\Bytes Received/sec`},
	{cNetTx, `\Network Interface(*)\Bytes Sent/sec`},

	{cVPGuestRun, `\Hyper-V Hypervisor Virtual Processor(*)\% Guest Run Time`},
	{cVMMemory, `\Hyper-V Dynamic Memory VM(*)\Physical Memory`},
	{cVMPressure, `\Hyper-V Dynamic Memory VM(*)\Average Pressure`},
	{cVSDReadBps, `\Hyper-V Virtual Storage Device(*)\Read Bytes/sec`},
	{cVSDWriteBps, `\Hyper-V Virtual Storage Device(*)\Write Bytes/sec`},
	{cVSDReadOps, `\Hyper-V Virtual Storage Device(*)\Read Operations/Sec`},
	{cVSDWriteOps, `\Hyper-V Virtual Storage Device(*)\Write Operations/Sec`},
	{cVSDLatency, `\Hyper-V Virtual Storage Device(*)\Latency`},
	{cVSDQueue, `\Hyper-V Virtual Storage Device(*)\Queue Length`},
	{cVNARx, `\Hyper-V Virtual Network Adapter(*)\Bytes Received/sec`},
	{cVNATx, `\Hyper-V Virtual Network Adapter(*)\Bytes Sent/sec`},
}

// totalInstance is the aggregate instance PDH adds to most multi-instance sets.
const totalInstance = "_Total"
