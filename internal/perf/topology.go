package perf

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"

	"ovc-agent/internal/hyperv"
)

// Topology maps Hyper-V counter instances to VMs. Hyper-V names its instances
// differently per counter set:
//
//	Virtual Processor        "<vm name>:Hv VP <n>"
//	Dynamic Memory VM        "<vm name>"
//	Virtual Storage Device   the VHD path, separators turned into '-'
//	Virtual Network Adapter  "<vm name>_<adapter name>_<vm guid>--<adapter guid>"
//
// Name-keyed instances only resolve when the VM name is unique on the host;
// disks resolve by path and NICs by GUID, so they never depend on names.
type Topology struct {
	vms        map[string]TopoVM   // by lowercase VM id
	idsByName  map[string][]string // lowercase name -> VM ids
	diskByKey  map[string]TopoDisk // normalized path -> disk
	diskByFile map[string][]TopoDisk
	vmByGUID   map[string]string // lowercase VM or adapter GUID -> VM id
	volumes    map[string]string // volume key (see volumeKey) -> label / CSV name
}

type TopoVM struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	MemoryAssigned float64 `json:"memoryAssigned"`
}

type TopoDisk struct {
	VMID string `json:"vmId"`
	Path string `json:"path"`
}

// TopoVolume names a host volume: a drive letter ("C:") with its label, or a
// CSV mount path with the CSV name.
type TopoVolume struct {
	Path  string `json:"path"`
	Label string `json:"label"`
}

type TopoNIC struct {
	VMID string `json:"vmId"`
	ID   string `json:"id"` // "Microsoft:<vm guid>\<adapter guid>"
}

var guidRe = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

func NewTopology(vms []TopoVM, disks []TopoDisk, nics []TopoNIC) *Topology {
	t := &Topology{
		vms:        make(map[string]TopoVM, len(vms)),
		idsByName:  make(map[string][]string, len(vms)),
		diskByKey:  make(map[string]TopoDisk, len(disks)),
		diskByFile: make(map[string][]TopoDisk, len(disks)),
		vmByGUID:   make(map[string]string, len(vms)+len(nics)),
	}
	for _, vm := range vms {
		id := strings.ToLower(vm.ID)
		vm.ID = id
		t.vms[id] = vm
		name := strings.ToLower(vm.Name)
		t.idsByName[name] = append(t.idsByName[name], id)
		t.vmByGUID[id] = id
	}
	for _, d := range disks {
		if d.Path == "" {
			continue
		}
		d.VMID = strings.ToLower(d.VMID)
		t.diskByKey[normalizeStorageKey(d.Path)] = d
		file := normalizeStorageKey(fileName(d.Path))
		t.diskByFile[file] = append(t.diskByFile[file], d)
	}
	for _, n := range nics {
		vmID := strings.ToLower(n.VMID)
		for _, g := range guidRe.FindAllString(n.ID, -1) {
			t.vmByGUID[strings.ToLower(g)] = vmID
		}
	}
	return t
}

// volumeKey matches a LogicalDisk instance to a TopoVolume path.
func volumeKey(p string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(p), `\`))
}

// VolumeLabel returns the label of a LogicalDisk instance ("C:",
// "C:\ClusterStorage\Volume1"), or "" when it has none. Safe on a nil Topology.
func (t *Topology) VolumeLabel(instance string) string {
	if t == nil {
		return ""
	}
	return t.volumes[volumeKey(instance)]
}

// VM returns the topology entry for a VM id.
func (t *Topology) VM(id string) (TopoVM, bool) {
	vm, ok := t.vms[id]
	return vm, ok
}

// vmByName resolves a name-keyed instance; ok is false when the name is unknown
// or shared by several VMs.
func (t *Topology) vmByName(name string) (id string, ok bool) {
	ids := t.idsByName[strings.ToLower(strings.TrimSpace(name))]
	if len(ids) != 1 {
		return "", false
	}
	return ids[0], true
}

// diskFor resolves a Virtual Storage Device instance: the full normalized path,
// then a unique file name, then a path suffix (PDH truncates long instance names).
func (t *Topology) diskFor(instance string) (TopoDisk, bool) {
	key := normalizeStorageKey(instance)
	if d, ok := t.diskByKey[key]; ok {
		return d, true
	}
	if ds := t.diskByFile[normalizeStorageKey(fileName(instance))]; len(ds) == 1 {
		return ds[0], true
	}
	var match TopoDisk
	found := 0
	for k, d := range t.diskByKey {
		if strings.HasSuffix(k, "-"+key) || strings.HasSuffix(key, "-"+k) {
			match = d
			found++
		}
	}
	return match, found == 1
}

// vmForNIC resolves a Virtual Network Adapter instance through the VM or adapter
// GUID it carries. Host (management OS) vNICs carry no VM GUID and do not match.
func (t *Topology) vmForNIC(instance string) (string, bool) {
	for _, g := range guidRe.FindAllString(instance, -1) {
		if id, ok := t.vmByGUID[strings.ToLower(g)]; ok {
			return id, true
		}
	}
	return "", false
}

// vpInstanceVM returns the VM name of a Virtual Processor instance
// ("web-01:Hv VP 0" -> "web-01"), or "" for anything else (e.g. _Total).
func vpInstanceVM(instance string) string {
	i := strings.LastIndex(instance, ":Hv VP")
	if i <= 0 {
		return ""
	}
	return instance[:i]
}

// normalizeStorageKey turns a VHD path and its counter instance into the same
// key: lowercase, separators as '-', no \\?\ prefix, single dashes, and PDH's
// replacement characters ("(" -> "[", ")" -> "]", "#" -> "_") folded back.
func normalizeStorageKey(s string) string {
	s = strings.ToLower(strings.Trim(strings.TrimSpace(s), `"`))
	s = strings.NewReplacer(`\`, "-", "/", "-", "[", "(", "]", ")", "#", "_").Replace(s)
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	s = strings.TrimPrefix(s, "-?-")
	s = strings.TrimPrefix(s, "?-")
	return strings.Trim(s, "-")
}

func fileName(p string) string {
	return path.Base(strings.ReplaceAll(p, `\`, "/"))
}

// topologyScript lists the VMs, their disks and their network adapters, plus
// the host volume labels and CSV names, in one PowerShell call. It runs every
// topologyRefresh, never on the sampling path.
const topologyScript = `
$ErrorActionPreference = 'SilentlyContinue'
$vms = @(Get-VM)
$volumes = @(Get-Volume | Where-Object { $_.DriveLetter -and $_.FileSystemLabel } | ForEach-Object { [pscustomobject]@{ path = "$($_.DriveLetter):"; label = $_.FileSystemLabel } })
try {
    $volumes += @(Get-ClusterSharedVolume -ErrorAction Stop | ForEach-Object { [pscustomobject]@{ path = "$(@($_.SharedVolumeInfo)[0].FriendlyVolumeName)"; label = $_.Name } })
} catch {}
[pscustomobject]@{
    vms   = @($vms | ForEach-Object { [pscustomobject]@{ id = "$($_.Id)"; name = $_.Name; memoryAssigned = [double]$_.MemoryAssigned } })
    disks = @($vms | Get-VMHardDiskDrive | ForEach-Object { [pscustomobject]@{ vmId = "$($_.VMId)"; path = "$($_.Path)" } })
    nics  = @($vms | Get-VMNetworkAdapter | ForEach-Object { [pscustomobject]@{ vmId = "$($_.VMId)"; id = "$($_.Id)" } })
    volumes = $volumes
} | ConvertTo-Json -Depth 4 -Compress
`

// FetchTopology runs topologyScript and builds the Topology.
func FetchTopology(ctx context.Context) (*Topology, error) {
	out, err := hyperv.RunPowerShell(ctx, topologyScript)
	if err != nil {
		return nil, err
	}
	out = bytes.TrimSpace(bytes.TrimPrefix(out, []byte("\xef\xbb\xbf")))
	var raw struct {
		VMs     json.RawMessage `json:"vms"`
		Disks   json.RawMessage `json:"disks"`
		NICs    json.RawMessage `json:"nics"`
		Volumes json.RawMessage `json:"volumes"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parse topology: %w", err)
	}
	var (
		vms     []TopoVM
		disks   []TopoDisk
		nics    []TopoNIC
		volumes []TopoVolume
	)
	if err := decodeList(raw.VMs, &vms); err != nil {
		return nil, fmt.Errorf("parse topology vms: %w", err)
	}
	if err := decodeList(raw.Disks, &disks); err != nil {
		return nil, fmt.Errorf("parse topology disks: %w", err)
	}
	if err := decodeList(raw.NICs, &nics); err != nil {
		return nil, fmt.Errorf("parse topology nics: %w", err)
	}
	if err := decodeList(raw.Volumes, &volumes); err != nil {
		return nil, fmt.Errorf("parse topology volumes: %w", err)
	}
	t := NewTopology(vms, disks, nics)
	t.volumes = make(map[string]string, len(volumes))
	for _, v := range volumes {
		if v.Path != "" && v.Label != "" {
			t.volumes[volumeKey(v.Path)] = v.Label
		}
	}
	return t, nil
}

// decodeList accepts a JSON array, a single object (PowerShell unwraps
// one-element arrays) or null.
func decodeList[T any](raw json.RawMessage, out *[]T) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if raw[0] == '[' {
		return json.Unmarshal(raw, out)
	}
	var one T
	if err := json.Unmarshal(raw, &one); err != nil {
		return err
	}
	*out = []T{one}
	return nil
}
