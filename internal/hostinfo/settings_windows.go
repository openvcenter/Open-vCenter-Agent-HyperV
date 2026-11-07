//go:build windows

package hostinfo

import (
	"errors"
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	modpowrprof               = windows.NewLazySystemDLL("powrprof.dll")
	procPowerGetActiveScheme  = modpowrprof.NewProc("PowerGetActiveScheme")
	procPowerReadFriendlyName = modpowrprof.NewProc("PowerReadFriendlyName")
	procLocalFree             = modkernel32.NewProc("LocalFree")
)

// PowerPlan is the active power scheme.
type PowerPlan struct {
	Name string `json:"name"`
	GUID string `json:"guid"`
}

// ActivePowerPlan reads the active power scheme via powrprof.dll (the same data
// as `powercfg /getactivescheme`, without spawning a process).
func ActivePowerPlan() (PowerPlan, error) {
	var guid *windows.GUID
	if r, _, _ := procPowerGetActiveScheme.Call(0, uintptr(unsafe.Pointer(&guid))); r != 0 {
		return PowerPlan{}, fmt.Errorf("PowerGetActiveScheme: %w", windows.Errno(r))
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(guid)))

	plan := PowerPlan{GUID: formatGUID(*guid)}

	// First call returns the buffer size in bytes, the second fills it (UTF-16).
	var size uint32
	if r, _, _ := procPowerReadFriendlyName.Call(0, uintptr(unsafe.Pointer(guid)), 0, 0, 0, uintptr(unsafe.Pointer(&size))); r != 0 || size < 2 {
		return plan, nil
	}
	buf := make([]uint16, size/2)
	if r, _, _ := procPowerReadFriendlyName.Call(0, uintptr(unsafe.Pointer(guid)), 0, 0,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size))); r == 0 {
		plan.Name = windows.UTF16ToString(buf)
	}
	return plan, nil
}

// formatGUID renders a GUID the way powercfg does: lower case, no braces.
func formatGUID(g windows.GUID) string {
	return strings.ToLower(strings.Trim(g.String(), "{}"))
}

// CoreIsolation is the configured Virtualization Based Security / Memory
// Integrity (HVCI) state from the DeviceGuard registry keys.
type CoreIsolation struct {
	VBS  bool `json:"vbs"`
	HVCI bool `json:"hvci"`
}

// CoreIsolationConfig reads the DeviceGuard registry values. Absent values
// mean disabled.
func CoreIsolationConfig() CoreIsolation {
	const dg = `SYSTEM\CurrentControlSet\Control\DeviceGuard`
	return CoreIsolation{
		VBS:  regDWORD(dg, "EnableVirtualizationBasedSecurity") == 1,
		HVCI: regDWORD(dg+`\Scenarios\HypervisorEnforcedCodeIntegrity`, "Enabled") == 1,
	}
}

// PageFileSettings reads the configured page files from the registry.
func PageFileSettings() (PageFileConfig, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Control\Session Manager\Memory Management`, registry.QUERY_VALUE)
	if err != nil {
		return PageFileConfig{}, err
	}
	defer k.Close()

	entries, _, err := k.GetStringsValue("PagingFiles")
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return PageFileConfig{}, err
	}
	return ParsePagingFiles(entries), nil
}

// WirelessLANInstalled reports whether the WLAN AutoConfig service exists,
// which the Wireless LAN Service feature installs (avoids Get-WindowsFeature).
func WirelessLANInstalled() bool {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Services\WlanSvc`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	k.Close()
	return true
}

// IPv6DisabledComponents returns the host-wide Tcpip6 DisabledComponents mask
// (0 = IPv6 fully enabled, the default when the value is absent; 0xFF =
// disabled on every interface).
func IPv6DisabledComponents() uint32 {
	return uint32(regDWORD(`SYSTEM\CurrentControlSet\Services\Tcpip6\Parameters`, "DisabledComponents"))
}

// regDWORD reads an HKLM integer value, or 0 when the key or value is missing.
func regDWORD(path, name string) uint64 {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE)
	if err != nil {
		return 0
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue(name)
	if err != nil {
		return 0
	}
	return v
}
