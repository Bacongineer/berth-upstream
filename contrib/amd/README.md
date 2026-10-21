# Linux AMD support and validation

Experimental support for **discrete RDNA3 and RDNA4**, using the existing
`gpu.Probe` interface. No ROCm, amd-smi, CGO, or device-context dependency in Berth.
The adapter does not allocate GPU memory or change driver settings.

## Selection and requirements

```sh
berth daemon --gpu-backend amd --gpu 0
berth service install --gpu-backend amd --gpu 0 --harbor
berth status --format json
```

AMD indices are DRM `cardN` numbers, retained even when unsupported cards are
excluded; they are not compacted and are not HIP device ordinals. Inventory and
process clients are joined by normalized PCI address, including the domain.
`auto` selects AMD when supported AMD cards exist and nvidia-smi is absent; if
both are present it requires an explicit backend. NVIDIA's indices and optional
streaming implementation are unchanged. The swap bench accepts `-gpu-backend` too.

Supported GC IPs are 11.0.0, 11.0.2, 11.0.3 (RDNA3 discrete) and 12.0.0, 12.0.1
(RDNA4 discrete). Unknown IPs and APUs, including Phoenix 11.0.1/4 and 11.5 GPUs,
are excluded, not scheduled as dedicated-VRAM devices. The kernel must expose
`ip_discovery/die/0/GC/0/{major,minor,revision}`, `mem_info_vram_{total,used}` and
DRM client memory counters. Use an amdgpu-enabled kernel/firmware supporting the
actual card; this feature does not install or upgrade drivers.

Run Berth on the host with its real `/proc` and `/sys` views, normally as the
same user as its engines. Restrictive procfs permissions, a PID namespace, or
unobservable cross-user/container processes limit attribution. Do not solve that
by granting blanket root privileges; first check process visibility and run the
opt-in validation on the target deployment.

## Memory and safety contract

- Card-wide total/used VRAM comes from the driver, in bytes. Free is their
  non-negative difference; GTT/system RAM is never added to dedicated VRAM.
- Per-process data uses resident VRAM (`drm-resident-vram`, falling back to
  legacy `drm-memory-vram`), never requested-buffer totals. The two resident
  spellings are aliases and are not added together.
- Duplicated/inherited DRM descriptors are deduplicated by PCI device and client
  ID. When a client is shared between PIDs, the lowest observed PID represents
  it; attribution to separate leases is inherently ambiguous.
- Shared buffer bytes are discounted from per-client residency because fdinfo
  has no object identity with which to deduplicate them across clients. Reported
  process use is consequently a private-resident lower bound; shared and other
  unattributed allocations stay in the card-wide baseline. On older kernels,
  unmapped GEM handles can also exist only in that baseline until GPU-VA mapping.
- Same-user PIDs whose fdinfo is inaccessible appear as `unobserved_pids` on the
  scheduled GPU in status JSON and as a human status warning. Previously observed
  ownership is retained; unreadable is never treated as a confirmed empty scan.
  Unreadable foreign-user allocations remain in the baseline without PID details.
- Failed or stale AMD observations block new admissions. A sampled PID/start-time
  mismatch invalidates the read instead of assigning old memory to a reused PID.
- Sized admissions must fit both the lease budget and fresh driver free bytes.
  Release/park confirmation requires the unattributed driver memory to return to
  the lease's pre-load baseline, as well as the existing process and read-order
  checks. Missing process data alone cannot confirm release or a park.
- This is conservative accounting, not an allocation reservation or an atomic
  driver snapshot. Changing shared/unobservable background allocations can delay
  confirmation. Whole-card leases retain Berth's existing report-only contested
  grant behavior: they do not promise an otherwise empty card or isolate engines.
  Configure engine device placement separately and use sized requests when the
  required footprint is known. Stowaways are never stopped.
- Optional temperature, utilization, power, clocks and PCIe readings use sysfs/
  hwmon. Unsupported values are -1. RPM/PWM are not mislabelled as fan percent.

## Milestones and repeatable checks

### 1. Unchanged baseline

Run the repository's proof, exact lint, and Windows cross-build/vet commands from
AGENTS.md before changes. These passed on the starting tree. Go 1.27.1 was used;
no dependency, Go version, linter version, or system package changes were needed.

### 2. Read-only observation

```sh
BERTH_TEST_AMD_LIVE=1 go test -v ./internal/gpu -run TestAMDLiveReadOnly -count=1
```

This only inspects host sysfs/procfs and defaults to card0. It never allocates or
unloads. On 2026-10-22, Linux 6.12.107+deb13-amd64 exposed the W7900's driver totals
and existing ROCm Python and Vulkan llama-server clients at PCI 0000:03:00.0. The
co-located 780M was excluded. Some systemd/SSH PIDs were inaccessible and were
reported explicitly. Observation alone does not validate engine unload behavior.

### 3. Fixture and broker safety gates

```sh
go test ./internal/gpu ./internal/broker ./internal/cli ./contrib/bench/swap
go test -race ./internal/gpu ./internal/broker ./internal/cli
```

Fixtures cover all five admitted GC IPs, legacy/modern resident counters, PCI
domains, multi-card numbering, APU exclusion, shared/duplicate clients, malformed
data, unit overflow, permissions, missing optional metrics and cancellation.
Broker regressions cover driver-delayed release, disappearing/unreadable PIDs,
stale/failed observations, PID reuse, park residual confirmation and unrelated
memory releases that must not conceal lingering buffers. Service tests check that
AMD/card flags reach the generated daemon unit, not the gateway or harbor.

### 4. Opt-in real allocation and handoff

**Ask the operator first.** This allocates actual GPU memory; it is not a read-only
check. Build a scratch binary, identify the matching render node by PCI device,
and ensure at least 128 MiB is free:

```sh
go build -o /tmp/opencode/berth-amd ./cmd/berth
python3 contrib/amd/check.py --allow-allocation \
  --berth /tmp/opencode/berth-amd --gpu 0 --render-node /dev/dri/renderD128 \
  --output /tmp/opencode/berth-amd-validation
```

The Python standard-library helper starts a separate daemon/socket/config with
engine discovery and claims disabled, and disables persistent usage files. It
maps a 64 MiB VRAM GEM buffer in a `berth run` child, confirms measured ownership,
requests a higher-priority second child, and checks that the first lease is freed
before the second is granted. Both children and the daemon are its own processes;
it never connects to installed services or stops existing GPU workloads. Evidence
is `report.json` plus logs in the requested directory. Use a new output directory
for each run. This validates the driver/lease path, not model correctness or a
specific engine's ROCm park/resume implementation.

**Hardware evidence:** W7900 (RDNA3/gfx1100), kernel 6.12.107+deb13-amd64: real
allocation, attribution, priority drain and measured handoff passed. RDNA4 is
fixture-tested but **has not been physically tested** in this environment.
NVIDIA compatibility is covered by its existing tests and Windows build/vet;
no NVIDIA card was available for a live regression test. Engine-specific live
park/resume, APU/UMA scheduling, AMD Windows and other AMD generations remain
outside this hardware validation claim.

### 5. Final regression gate

Run the full Linux proof and exact lint commands, Windows build/vet, and the
opt-in hardware check after the final source change. Keep all checks green;
record unrelated failures rather than changing unrelated code.

## References

- [Kernel AMDGPU sysfs memory counters](https://docs.kernel.org/gpu/amdgpu/driver-misc.html#gpu-memory-usage-information)
- [DRM client identity, residency, sharing and units](https://www.kernel.org/doc/html/latest/gpu/drm-usage-stats.html)
- [Linux AMDGPU GC 11 platform handling](https://github.com/torvalds/linux/blob/master/drivers/gpu/drm/amd/amdgpu/soc21.c)
- [Linux AMDGPU GC 12 platform handling](https://github.com/torvalds/linux/blob/master/drivers/gpu/drm/amd/amdgpu/soc24.c)
