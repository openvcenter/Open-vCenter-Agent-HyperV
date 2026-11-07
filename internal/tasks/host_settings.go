package tasks

import (
	"context"
	"encoding/json"

	"ovc-agent/internal/hostinfo"
	"ovc-agent/internal/hyperv"
)

// HostSettingsResult holds host configuration values, reported as-is (no
// pass/fail policy). Every block is best-effort: one that cannot be read is
// omitted and the backend keeps showing the rest.
type HostSettingsResult struct {
	PowerPlan     *hostinfo.PowerPlan      `json:"powerPlan,omitempty"`
	CoreIsolation *hostinfo.CoreIsolation  `json:"coreIsolation,omitempty"`
	PageFile      *hostinfo.PageFileConfig `json:"pageFile,omitempty"`
	WirelessLAN   *WirelessLANSetting      `json:"wirelessLan,omitempty"`
	IPv6          *IPv6Setting             `json:"ipv6,omitempty"`
	HyperV        *HyperVHostSettings      `json:"hyperv,omitempty"`
}

// WirelessLANSetting reports whether the Wireless LAN Service is installed.
type WirelessLANSetting struct {
	Installed bool `json:"installed"`
}

// IPv6Setting is the host-wide IPv6 state (per-NIC bindings are reported in
// host_inventory netAdapters).
type IPv6Setting struct {
	DisabledComponents uint32 `json:"disabledComponents"`
}

// HyperVHostSettings are the Get-VMHost settings shown on the host.
type HyperVHostSettings struct {
	NumaSpanning        bool                  `json:"numaSpanning"`
	EnhancedSessionMode bool                  `json:"enhancedSessionMode"`
	LiveMigration       LiveMigrationSettings `json:"liveMigration"`
}

// LiveMigrationSettings mirror the Get-VMHost migration properties.
type LiveMigrationSettings struct {
	Enabled              bool   `json:"enabled"`
	MaxVMMigrations      int    `json:"maxVmMigrations"`
	MaxStorageMigrations int    `json:"maxStorageMigrations"`
	UseAnyNetwork        bool   `json:"useAnyNetwork"`
	Authentication       string `json:"authentication"` // CredSSP | Kerberos
	Performance          string `json:"performance"`    // TCPIP | Compression | SMB
}

// hyperVHostSettingsScript is the only PowerShell of the task: the migration
// authentication / performance options have no documented registry values.
const hyperVHostSettingsScript = `
$h = Get-VMHost -ErrorAction Stop
[PSCustomObject]@{
    numaSpanning        = [bool]$h.NumaSpanningEnabled
    enhancedSessionMode = [bool]$h.EnableEnhancedSessionMode
    liveMigration       = [PSCustomObject]@{
        enabled              = [bool]$h.VirtualMachineMigrationEnabled
        maxVmMigrations      = [int]$h.MaximumVirtualMachineMigrations
        maxStorageMigrations = [int]$h.MaximumStorageMigrations
        useAnyNetwork        = [bool]$h.UseAnyNetworkForMigration
        authentication       = [string]$h.VirtualMachineMigrationAuthenticationType
        performance          = [string]$h.VirtualMachineMigrationPerformanceOption
    }
} | ConvertTo-Json -Depth 3 -Compress
`

// handleHostSettings collects the host configuration values. Everything but the
// Hyper-V block is read natively (powrprof.dll / registry).
func (d *Dispatcher) handleHostSettings(ctx context.Context) (*HostSettingsResult, error) {
	result := &HostSettingsResult{
		CoreIsolation: ptr(hostinfo.CoreIsolationConfig()),
		WirelessLAN:   &WirelessLANSetting{Installed: hostinfo.WirelessLANInstalled()},
		IPv6:          &IPv6Setting{DisabledComponents: hostinfo.IPv6DisabledComponents()},
	}

	if plan, err := hostinfo.ActivePowerPlan(); err == nil {
		result.PowerPlan = &plan
	} else {
		d.logger.Warn("host_settings: power plan unavailable", "error", err)
	}

	if pf, err := hostinfo.PageFileSettings(); err == nil {
		result.PageFile = &pf
	} else {
		d.logger.Warn("host_settings: page file settings unavailable", "error", err)
	}

	if output, err := hyperv.RunPowerShell(ctx, hyperVHostSettingsScript); err != nil {
		d.logger.Warn("host_settings: Get-VMHost failed", "error", err)
	} else {
		var hv HyperVHostSettings
		if err := json.Unmarshal(trimPowerShellOutput(output), &hv); err != nil {
			d.logger.Warn("host_settings: Get-VMHost parse failed", "error", err)
		} else {
			result.HyperV = &hv
		}
	}

	return result, nil
}

func ptr[T any](v T) *T { return &v }
