# ovc-agent - the Hyper-V agent for Open vCenter

This is the **Hyper-V** agent (`ovc-agent`, Go, build for WINDOWS only).
Other agents (libvirt / KVM) will follow that implement exactly the same
`<hostid>.*` queue protocol described below - the backend already models
Host/Cluster with a `hypervisor` discriminator and the messages are
hypervisor-agnostic.

Stack: Go (Windows only)
Message queue: RabbitMQ
Backend communication: exclusively via RabbitMQ
Build: `./build.sh` (mac/Linux) or `.\build.ps1` (Windows) → `ovc-agent.exe` at the repo root.
Version: `AgentVersion` constant in `internal/tasks/agent_status.go` (single source).
Local job persistence: SQLite (`modernc.org/sqlite`, pure Go)

Contract source of truth: `../ovc-backend/app/messaging/` (protocol.py, queues.py)
and `../ovc-backend/app/services/inventory.py`.

## Conventions

All code, comments, identifiers, commit messages, docs and log strings are in
**English**, regardless of the language a contributor chats in.

## RabbitMQ queues (per host)

- `<hostid>.request` - backend publishes `AgentRequest`, the agent consumes
- `<hostid>.response` - the agent publishes `AgentResponse`, the backend consumes
- `<hostid>.agent_status` - last-value, the agent publishes periodically
- `<hostid>.vm_inventory` - last-value
- `<hostid>.host_inventory` - last-value (hardware)
- `<hostid>.template_inventory` - last-value
- `<hostid>.iso_inventory` - last-value
- `<hostid>.settings_inventory` - last-value (host settings)

Last-value queues: `x-max-length=1`, `x-overflow=drop-head`.

The worker subprocess publishes `response` and the state queues via
`Publisher.PublishConfirmed` (publisher confirms + `mandatory`): it blocks until
the broker `ack`s, and otherwise logs `NOT confirmed by broker` with the reason
(nack / unroutable / timeout). Needed because the worker calls `os.Exit` a few ms
after publishing - an unconfirmed publish is lost on a slow link. Progress
updates stay fire-and-forget (`PublishTo`).

## Message envelope

`AgentRequest`: `{ id, function, params, requested_by, issued_at, timeout_seconds }`
`AgentResponse`: `{ id, function, status, progress, result, error, vm_id, vm_state, finished_at }`
`status` ∈ `running | succeeded | failed`.

## Flow

The agent listens on `<hostid>.request`. On receipt it validates that the
`function` exists, persists a job in SQLite and spawns a worker subprocess
(`ovc-agent.exe job <id>`) to run it. The `jobqueue` controls concurrency (reads
are free, writes are mutually exclusive per VM, a host-write blocks everything).

**Job timeout.** `timeout_seconds` is the job's budget, computed by the backend
(`ovc-backend` `app/task_timeouts.py`; it scales `vm_create` / `vm_edit` /
`vm_clone` / `vm_move` / `vm_rename` / `vm_export_template` with the disk sizes). It is stored on the job (`jobs.timeout_seconds`); the
`jobqueue.Monitor` kills the worker after it and `Dispatch` gives the handler's
context slightly less (minus 1 min, or 10% for short budgets) so it can report
the failure first. Handlers run all their PowerShell under that context. When it
is 0/absent the agent falls back to its per-kind tables (`monitor.go`
`taskTimeouts` / `actionTimeouts`, and the `Dispatch` minimums: 30 min for
`vm_create` / `vm_edit` / `vm_clone` and the long `vm_management` actions).

### VM functions (module `vm_management`)

`vm_create`, `vm_edit`, `vm_shutdown`, `vm_start`, `vm_stop`, `vm_restart`,
`vm_delete`, `vm_inventory` (+ snapshots, migration, dvd, HA, export template,
rename, move, clone, notes, batch start/stop).

**Disk creation** (`vm_create` disks, `vm_edit` `add_disk`) goes through WMI
(`Msvm_ImageManagementService.CreateVirtualHardDisk`, `hyperv.CreateVHDWithProgress`)
instead of `New-VHD`, so the job's `PercentComplete` is published as task progress
(`Creating disk 1/2: <path> (42%)`). The disks share 0-90% of `vm_create` and 0-99%
of `vm_edit`. If WMI cannot start the job it falls back to `New-VHD` (no progress,
logged as a warning).

**`vm_export_template`** exports the VM, then converts every Fixed `.vhd`/`.vhdx`
in the exported folder to Dynamic in place (same filename kept, so the `.vmcx`
stays valid) to shrink the template store - skipped if the export contains
checkpoint/`.avhdx` disks, best-effort/non-fatal per disk. It then writes
`ovc-template-metadata.json` (stable UUID, name, `notes` from the params, source
VM, `exported_at` timestamp, host, and `size` = provisioned disk size).
`template_inventory` reads that JSON and reports `sizeBytes` (provisioned, from
`Get-OvcTemplateSize` or the metadata `size`), `diskSizeBytes` (real on-disk
folder footprint, measured natively in Go), `notes`, `createdAt` (the metadata
`exported_at`), and `cpuCount` / `memoryMb` (from `Compare-VM` on the `.vmcx`).

**`vm_clone`** (`clone_type` `imported` = clone an off VM, `exported` = deploy an
exported template folder) resolves its own destination folder from
`destination_storage` (same `resolveVMFolder` rules as `vm_create`) and, for
`imported`, its own scratch export folder - the backend sends neither `path` nor
`export_folder_root`. For `exported`, the deployed VM's Dynamic disks are
converted to Fixed after import by default (best-effort/non-fatal per disk,
skipped when the full size would not fit); `expand_disks: false` keeps them Dynamic.

### host functions (module `host_management`)

`host_hwinventory`, `host_update_agent` (agent upgrade), `host_restart_agent`
(+ cluster operations: suspend / resume / restart).

Artifact download now boils down to the **agent upgrade** (`host_update_agent`):
it downloads the new binary, verifies the checksum, enters drain mode and swaps
itself in.

## Periodic publications

`agent_status`, `vm_inventory`, `host_hwinventory` (-> host_inventory),
`template_inventory`, `iso_inventory`, `host_settings` (-> settings_inventory).
Configurable intervals (`refresh_interval_vms`, `refresh_interval_host`,
`refresh_interval_settings`).

`host_settings` (`internal/tasks/host_settings.go`, every 30 min by default)
reports host configuration values as-is, no pass/fail: power plan (powrprof.dll),
core isolation VBS/HVCI, page files, Wireless LAN service, host-wide IPv6
`DisabledComponents` (registry, `internal/hostinfo/settings_windows.go`), and
NUMA spanning / enhanced session mode / live migration settings (one `Get-VMHost`).
Each block is best-effort and omitted when it cannot be read. Per-NIC IPv6 binding
and VMQ (`ipv6Enabled` / `vmqEnabled`, omitted when unknown) are in the
`host_inventory` NICs instead.

`agent_status` is the liveness heartbeat: it runs on its own short interval
(`heartbeat_interval`, default 60s), NOT on `refresh_interval_host`. The backend
marks the host offline if the last `agent_status` is older than
`agent_offline_after_seconds` (120s), so the heartbeat must stay well below that
even while the heavy host inventory runs every 10 min.

`host_hwinventory`: the collector assembles the rich `HardwareInventoryResult`
struct (PowerShell + native Go), but its `MarshalJSON` (`marshal_hardware.go`)
reshapes it into the canonical form the backend expects -
`cpu{model,sockets,cores,logical}`, `memoryBytes`,
`storage[]{path,label,totalBytes,freeBytes}`, `os`, `network:[]`,

- `system`/`bootTime`/`load`/`cluster`/`hyperv`. `totalVMs`/`uptimeDays`/
  `autoBalancer*` are collected but not sent.

The `Scheduler` creates internal jobs with `TaskID` `refresh-<kind>-<ts>`
(`jobqueue.PeriodicRefreshTaskID` / `IsPeriodicRefreshTaskID`). Since **no backend
task** is waiting on them, the worker publishes only to the state queue
(`<hostid>.<kind>`) - it does **not** send an `AgentResponse` on `<hostid>.response`.

`host_metrics` + `vm_metrics` - quick metrics (CPU / memory / disk / network),
published by `perf.Sampler` (`internal/perf`) in the service process, not by jobs.
It reads PDH counters (`pdh.dll`, `PdhAddEnglishCounterW`) every 5 s and publishes
the mean and peak once per collection interval. Plain durable queues (NOT
last-value): a short time-series. No PowerShell on the sampling path; a
PowerShell call refreshes the VM topology - which instance belongs to which VM -
every 30 min, or sooner (at most once a minute) when a counter instance appears
that does not map. Instances still unmapped right after a refresh (an ISO, a
host vNIC) are ignored until the next one:

- host: CPU from `Hyper-V Hypervisor Logical Processor(_Total)` (the `Processor`
  sets only see the root partition), memory from `GlobalMemoryStatusEx`, disks
  per volume from `LogicalDisk(*)` (C:, D:, CSV paths; unlettered volumes only
  count in the total) with a `label` (the volume label, or the CSV name for a
  CSV path; from the topology script's `Get-Volume` / `Get-ClusterSharedVolume`),
  network from `Network Interface(*)`.
- VMs (every running VM): CPU from `Hyper-V Hypervisor Virtual Processor`
  (`<vm>:Hv VP <n>`, by name), memory from `Hyper-V Dynamic Memory VM` (by name;
  falls back to `MemoryAssigned`), disks from `Hyper-V Virtual Storage Device`
  (by VHD path), network from `Hyper-V Virtual Network Adapter` (by VM/adapter
  GUID). A name shared by two VMs leaves their CPU/memory null.

The collection interval comes from the backend's Application Preferences: the
`set_metrics_config` request (`params: {interval_seconds}`) is handled directly
by the service loop (no job, no `AgentResponse`), saved as `metrics_interval` in
`config.backend.ini` and reported back as `metrics_interval` in `agent_status`.
Default 300 s until the backend sends one.

`vm_inventory` also carries, per VM, two best-effort fields that are omitted when
unknown (the backend then keeps the last stored value):

- `guestOs` - guest OS name from the KVP integration service
  (`Msvm_SummaryInformation.GuestOperatingSystem`, one CIM enumeration in the
  basic block). Only reported while the VM runs with integration services up.
- `thumbnail` - 320x240 JPEG of the console (base64 in JSON), Running VMs only
  (`internal/tasks/vm_thumbnail.go`). One extra PowerShell spawn calls
  `Msvm_VirtualSystemManagementService.GetVirtualSystemThumbnailImage` against
  each VM's *realized* settings and returns raw RGB565 frames (4-byte header +
  `w*h*2` bytes); Go converts them to JPEG. The spawn is skipped when no VM is
  running; a failed capture never fails the inventory.

`agent_status` also reports the agent type (`hypervisor: "hyperv"`,
`agent_type: "ovc-agent-hyperv"`).

## Startup

Checks that the Hyper-V role is installed. If not, it writes `agent_failed.log`
in the binary's folder and exits.

## Config

The agent has NO fixed install folder: the binary's folder is the default folder
(`config.ini`, `agent.log`, `jobs.db`, and upgrade binaries all live there).
Config is read from `config.ini` (no registry, no env), then overlaid by
`config.backend.ini` (same folder, same `[agent]` keys, wins over `config.ini`):
settings the backend pushed, written by the agent via `config.SaveBackendValue`
(atomic replace). A missing or unreadable `config.backend.ini` is ignored. Example:

```ini
[agent]
host_id = <id generated by the backend when the host is registered>
rabbitmq_url = amqp://user:password@rabbit:5672/
log_level = info
refresh_interval_vms = 180
refresh_interval_host = 600
refresh_interval_settings = 1800
heartbeat_interval = 60
metrics_interval = 300
template_path = D:\HyperV\TEMPLATES
local_iso_path = D:\HyperV\ISOS
aditional_vm_storage = E:\HyperV
```
