// Package protocol mirrors the RabbitMQ message contract defined by ovc-backend
// in app/messaging/protocol.py and app/messaging/queues.py. The backend is the
// source of truth; keep these types in sync with it.
package protocol

import (
	"encoding/json"
	"time"
)

// Now returns an RFC3339 timestamp in UTC, matching the backend's isoformat.
func Now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// AgentRequest is the JSON the backend writes to <hostid>.request.
type AgentRequest struct {
	ID          string          `json:"id"` // == Task.id / AMQP correlation id
	Function    string          `json:"function"`
	Params      json.RawMessage `json:"params"`
	RequestedBy string          `json:"requested_by"`
	IssuedAt    string          `json:"issued_at"`
	// TimeoutSeconds is how long the job may run, computed by the backend (disk
	// work scales it with the disk sizes). 0 = the agent's per-kind default.
	TimeoutSeconds int `json:"timeout_seconds"`
}

// AgentResponse is the JSON the agent writes to <hostid>.response.
//
// status is one of "running" | "succeeded" | "failed". The backend
// (services/inventory.py::apply_response) only persists result when it is a
// string; for VM state changes it reads vm_id + vm_state instead.
type AgentResponse struct {
	ID         string  `json:"id"`
	Function   string  `json:"function"`
	Status     string  `json:"status"`
	Progress   *int    `json:"progress"`
	Result     any     `json:"result"`
	Error      *string `json:"error"`
	VMID       *string `json:"vm_id"`
	VMState    *string `json:"vm_state"`
	FinishedAt *string `json:"finished_at"`
}

// Wire status values.
const (
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
)

// NormalizeStatus maps the agent's internal lifecycle vocabulary onto the three
// values the backend understands.
func NormalizeStatus(s string) string {
	switch s {
	case "completed", "succeeded", "success", "ok":
		return StatusSucceeded
	case "failed", "error":
		return StatusFailed
	case "in_progress", "running", "pending", "queued":
		return StatusRunning
	default:
		return s
	}
}

// --- last-value inventory payloads (agent -> backend) ----------------------

type AgentStatusPayload struct {
	Version             string `json:"version"`
	VMRefreshInterval   int    `json:"vm_refresh_interval"`
	HostRefreshInterval int    `json:"host_refresh_interval"`
	Hypervisor          string `json:"hypervisor"`
	AgentType           string `json:"agent_type"`
	Hostname            string `json:"hostname,omitempty"`
	// FQDN and IP are agent-resolved. IP is the host's management/primary
	// address (the interface that owns the active default route) - never a
	// vMotion / live-migration / storage network.
	FQDN          string `json:"fqdn,omitempty"`
	IP            string `json:"ip,omitempty"`
	OSVersion     string `json:"os_version,omitempty"`
	UptimeSeconds int64  `json:"uptime_seconds,omitempty"`
	// MetricsInterval is the metrics collection window currently applied, so
	// the backend can push set_metrics_config when it differs from its setting.
	MetricsInterval int    `json:"metrics_interval"`
	ReportedAt      string `json:"reported_at"`
}

type VMInventoryPayload struct {
	ReportedAt string `json:"reported_at"`
	VMs        any    `json:"vms"`
}

type HostInventoryPayload struct {
	ReportedAt string `json:"reported_at"`
	Hardware   any    `json:"hardware"`
}

type ImageInventoryPayload struct {
	ReportedAt string `json:"reported_at"`
	Items      any    `json:"items"`
}

// --- quick metrics payloads (short time-series queues, NOT last-value) --------
// Published by internal/perf.Sampler once per collection window and consumed by
// ovc-backend app/services/inventory.py::apply_host_metrics / apply_vm_metrics.
// Values are the mean over the window's 5 s samples unless named *Max (peak).
// Rates are per second; nil means the counter was not available.

type HostMetricsPayload struct {
	ReportedAt         string          `json:"reported_at"`
	CPUPercent         *float64        `json:"cpuPercent"`
	CPUMaxPercent      *float64        `json:"cpuMaxPercent"`
	MemPercent         *float64        `json:"memPercent"`
	MemUsedBytes       *float64        `json:"memUsedBytes"`
	MemTotalBytes      *float64        `json:"memTotalBytes"`
	DiskReadBps        *float64        `json:"diskReadBps"`
	DiskWriteBps       *float64        `json:"diskWriteBps"`
	DiskReadIops       *float64        `json:"diskReadIops"`
	DiskWriteIops      *float64        `json:"diskWriteIops"`
	DiskReadLatencyMs  *float64        `json:"diskReadLatencyMs"`
	DiskWriteLatencyMs *float64        `json:"diskWriteLatencyMs"`
	DiskLatencyMaxMs   *float64        `json:"diskLatencyMaxMs"`
	NetRxBps           *float64        `json:"netRxBps"`
	NetTxBps           *float64        `json:"netTxBps"`
	Disks              []HostDiskStats `json:"disks"`
	Net                []HostNetStats  `json:"net"`
}

type HostDiskStats struct {
	Name           string   `json:"name"`
	ReadBps        *float64 `json:"readBps"`
	WriteBps       *float64 `json:"writeBps"`
	ReadIops       *float64 `json:"readIops"`
	WriteIops      *float64 `json:"writeIops"`
	ReadLatencyMs  *float64 `json:"readLatencyMs"`
	WriteLatencyMs *float64 `json:"writeLatencyMs"`
	QueueLength    *float64 `json:"queueLength"`
	BusyPercent    *float64 `json:"busyPercent"`
}

type HostNetStats struct {
	Name  string   `json:"name"`
	RxBps *float64 `json:"rxBps"`
	TxBps *float64 `json:"txBps"`
}

type VMMetricsPayload struct {
	ReportedAt string          `json:"reported_at"`
	VMs        []VMMetricStats `json:"vms"`
}

type VMMetricStats struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	CPUPercent    *float64      `json:"cpuPercent"`
	CPUMaxPercent *float64      `json:"cpuMaxPercent"`
	MemBytes      *float64      `json:"memBytes"`
	MemPressure   *float64      `json:"memPressure"`
	DiskReadBps   *float64      `json:"diskReadBps"`
	DiskWriteBps  *float64      `json:"diskWriteBps"`
	DiskReadIops  *float64      `json:"diskReadIops"`
	DiskWriteIops *float64      `json:"diskWriteIops"`
	DiskLatencyMs *float64      `json:"diskLatencyMs"`
	NetRxBps      *float64      `json:"netRxBps"`
	NetTxBps      *float64      `json:"netTxBps"`
	Disks         []VMDiskStats `json:"disks"`
}

type VMDiskStats struct {
	Path        string   `json:"path"`
	ReadBps     *float64 `json:"readBps"`
	WriteBps    *float64 `json:"writeBps"`
	ReadIops    *float64 `json:"readIops"`
	WriteIops   *float64 `json:"writeIops"`
	LatencyMs   *float64 `json:"latencyMs"`
	QueueLength *float64 `json:"queueLength"`
}

// MetricsConfigParams are the params of the set_metrics_config request: the
// collection window the backend's Application Preferences ask for.
type MetricsConfigParams struct {
	IntervalSeconds int `json:"interval_seconds"`
}
