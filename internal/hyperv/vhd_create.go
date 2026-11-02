package hyperv

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// CreateVHDWithProgress creates a VHD/VHDX through the Hyper-V WMI image management
// service (Msvm_ImageManagementService.CreateVirtualHardDisk). Unlike New-VHD, its job
// exposes PercentComplete, which is reported to onProgress (0-100) while a Fixed disk
// is being zero-filled. If the WMI call cannot start a job, it falls back to New-VHD
// (no progress); onFallback, if set, receives the reason.
func CreateVHDWithProgress(ctx context.Context, path string, sizeBytes int64, fixed bool, onProgress func(percent int), onFallback func(reason string)) error {
	// Msvm_VirtualHardDiskSettingData: Type 2 = Fixed, 3 = Dynamic; Format 2 = VHD, 3 = VHDX.
	diskType, newVHDSwitch := 3, "Dynamic"
	if fixed {
		diskType, newVHDSwitch = 2, "Fixed"
	}
	format := 3
	if strings.EqualFold(filepath.Ext(path), ".vhd") {
		format = 2
	}
	quotedPath := "'" + strings.ReplaceAll(path, "'", "''") + "'"

	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$path = %s
try {
    try {
        $ims = Get-WmiObject -Namespace root\virtualization\v2 -Class Msvm_ImageManagementService
        $sd = ([wmiclass]'\\.\root\virtualization\v2:Msvm_VirtualHardDiskSettingData').CreateInstance()
        $sd.Type = [uint16]%d
        $sd.Format = [uint16]%d
        $sd.Path = $path
        $sd.MaxInternalSize = [uint64]%d
        # 1 = WmiTextFormat.CimDtd20, the embedded-instance format the method expects
        $ret = $ims.CreateVirtualHardDisk($sd.GetText(1))
    } catch {
        $ret = $null
        $setupError = $_.Exception.Message
    }

    if ($null -ne $ret -and $ret.ReturnValue -eq 0) {
        [Console]::Out.WriteLine("PROGRESS:100")
        [Console]::Out.Flush()
    } elseif ($null -ne $ret -and $ret.ReturnValue -eq 4096) {
        $lastPct = -1
        while ($true) {
            $job = [wmi]$ret.Job
            $pct = [int]$job.PercentComplete
            if ($pct -ne $lastPct) {
                $lastPct = $pct
                [Console]::Out.WriteLine("PROGRESS:$pct")
                [Console]::Out.Flush()
            }
            # JobState 2 = New, 3 = Starting, 4 = Running; anything else is terminal
            if ($job.JobState -ne 2 -and $job.JobState -ne 3 -and $job.JobState -ne 4) { break }
            Start-Sleep -Seconds 2
        }
        if ($job.JobState -ne 7) {
            $msg = $job.ErrorDescription
            if (-not $msg) { $msg = "job ended in state $($job.JobState) (error code $($job.ErrorCode))" }
            throw $msg
        }
    } else {
        if ($null -ne $ret) { $setupError = "CreateVirtualHardDisk returned $($ret.ReturnValue)" }
        [Console]::Out.WriteLine("FALLBACK:$setupError")
        [Console]::Out.Flush()
        New-VHD -Path $path -SizeBytes %d -%s | Out-Null
    }
} catch {
    [Console]::Error.WriteLine("VHD_CREATE_FAILED: $($_.Exception.Message)")
    exit 1
}
`, quotedPath, diskType, format, sizeBytes, sizeBytes, newVHDSwitch)

	_, err := RunLongPowerShellStream(ctx, script, func(line string) {
		switch {
		case strings.HasPrefix(line, "PROGRESS:"):
			var pct int
			if _, scanErr := fmt.Sscanf(strings.TrimPrefix(line, "PROGRESS:"), "%d", &pct); scanErr == nil && onProgress != nil {
				onProgress(pct)
			}
		case strings.HasPrefix(line, "FALLBACK:"):
			if onFallback != nil {
				onFallback(strings.TrimPrefix(line, "FALLBACK:"))
			}
		}
	})
	return err
}
