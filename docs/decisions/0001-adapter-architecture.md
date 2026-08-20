# ADR 0001: Start with an Independent JSON-to-OTLP Adapter

Status: Proposed

Date: 2026-08-20

## Context

BlobFuse2 includes a preview companion process named `bfusemon`. It collects
BlobFuse component statistics, process CPU and virtual memory, and file-cache
events. Its only completed output is a set of rotating JSON files.

The desired outcome is vendor-neutral BlobFuse metrics that can be routed to
Azure Monitor, Prometheus, Grafana, Datadog, or other systems without adding a
backend-specific integration for each destination.

The ideal long-term implementation is a supported exporter contract inside
`bfusemon`. That requires acceptance and maintenance by the Microsoft-owned
BlobFuse project. The prototype must remain useful if no upstream change is
accepted.

The current source format has material constraints documented in
[the source contract](../source-contract.md): active files are incomplete JSON
arrays, records are delayed in memory, rotation is rename-based, and the schema
is private and unversioned.

## Decision Drivers

- Work with an unmodified, Microsoft-supported BlobFuse installation.
- Keep failures and resource overhead outside the BlobFuse I/O hot path.
- Deliver one standard telemetry stream instead of many backend integrations.
- Preserve a migration path to an upstream-native exporter.
- Detect private-schema changes rather than silently emitting incorrect data.
- Avoid taking ownership of a permanent BlobFuse fork unless necessary.
- Make deployment practical on Linux hosts and later in AKS.

## Decision

### Repository and Ownership

Build the prototype as an independent repository and executable. Do not fork or
vendor BlobFuse for the first milestone.

### Input Boundary

Read the existing rotating `bfusemon` JSON reports. Implement an incremental
decoder, rotation-aware watcher, and in-memory read watermark. Do not access
BlobFuse's private FIFOs in the first phase.

Version 0 requires one explicit PID and one empty, dedicated report directory
for the lifetime of that mount. Bind the source session to the host boot ID,
PID, and procfs process start ticks. A missing or changed identity ends the
session; do not follow PID reuse automatically.

Register directory notifications before enumeration. Open reports relative to
the validated directory descriptor, identify generations by device and inode,
and retain descriptors across renames. Scan oldest to newest in baseline mode,
then drain notifications and rescan until every known descriptor is at its last
complete-object boundary and no unseen generation remains. Those consumed
offsets form the atomic live cutover; never derive them from pathname file size.
The complete algorithm is normative in
[the source contract](../source-contract.md).

There is no complete aggregate snapshot: BlobFuse emits a component only when
that component changed, and a key might not exist until its operation first
occurs. Gauges found during initialization can be exported immediately as the
latest known state. A series first encountered after attachment follows the
same rule: a counter establishes a baseline without emitting an increment,
while a gauge publishes its observed value.

An explicit offline/backfill mode can process historical reports later. It is
not part of the initial live-monitoring path and may introduce durable replay
state independently.

### Output Boundary

Export metrics with OTLP over HTTP using Protobuf. The default destination is a
local OpenTelemetry Collector. OTLP/gRPC may be added later if users require it.

Export monotonic sums with cumulative temporality. The adapter configures this
explicitly rather than inheriting an SDK or environment default. After baseline
cutover, capture the adapter epoch immediately before constructing the
MeterProvider. That provider lifecycle defines the cumulative OTLP start time
for the run. A baseline alone produces no synthetic zero counter point; the
first live addition creates the first point in the new epoch. Adapter restart
creates a new epoch even if the monitored BlobFuse process continues.

Do not implement backend-specific exporters in the adapter. The Collector owns
authentication, TLS termination, enrichment, filtering, batching beyond the
local process, and routing to observability backends.

Do not expose a direct Prometheus/OpenMetrics endpoint in version 0. Cumulative
sums match Prometheus counter semantics, but transport still requires either an
enabled Prometheus OTLP receiver or an OpenTelemetry Collector exposing a
Prometheus endpoint.

### Signal Scope

Export aggregate metrics only. Decode path-bearing records but discard their
contents after advancing the live read watermark. Do not emit logs or traces in
the first release.

Use the fixed metric mapping in [metrics.md](../metrics.md). Dynamic conversion
of arbitrary source map keys into metric names or attributes is prohibited.

Version 0 validates the JSON-to-OTLP transport and metric contract; it is not a
claim of complete BlobFuse observability. In particular, the source lacks
filesystem error counts, request/retry counts at the Azure SDK/HTTP boundary,
and FUSE/storage latency histograms. The first upstream proposal will request
those producer-side signals rather than attempt to infer them in the adapter.

`azstorage` operation counters will not be presented as Azure request metrics:
they count storage-component method calls, not REST requests.

### Runtime

Implement the prototype in Go and distribute a single Linux executable.

Reasons:

- static deployment is suitable for VM and sidecar use;
- Go has maintained OpenTelemetry SDK and OTLP exporter packages;
- incremental JSON decoding and Linux file watching are well supported;
- the implementation can share concepts with BlobFuse without importing its
  internal Go packages;
- operational dependencies remain small.

The code must define its own source DTOs. Importing
`github.com/Azure/azure-storage-fuse/v2/internal/...` is prohibited.

Minimal procfs access is required to bind a PID to its process start identity.
Version 0 does not use procfs to produce process metrics. The complete command
line, timing, endpoint, size, and resource limits are defined in
[the version 0 configuration](../configuration.md).

### Source Report Security

Treat the report directory as sensitive because `bfusemon` records file and
blob paths. Version 0 trusts the kernel, root, and processes under the report
owner's UID, but not other local users or pathnames that can be substituted.
The adapter and `bfusemon` run with the same effective UID. Production requires
an absolute, dedicated `0700` directory and `umask 077`.

Open the directory and reports with descriptor-relative no-follow semantics.
Validate owner, mode, regular-file type, device, and inode through opened
descriptors at startup and after rotation. The development override may relax
owner and mode checks only; it cannot enable symlinks or pathname-only checks.

The adapter does not change source ownership or permissions and does not log
raw records, payload fragments, file paths, or blob names.

### State and Delivery

Maintain an in-memory read watermark per source session containing the file
generation and consumed byte offset. It prevents duplicate reads caused by
filesystem notifications and directory rescans during one adapter run; it is
not a durable delivery ledger. Advance it only after a valid observation has
been applied to local metric state.

After an adapter restart, discard the old watermark and repeat the startup scan.
Re-establish each counter from its latest retained value without exporting
historical increments, restore the latest known gauges, and then observe new
changes. Activity that occurred while the adapter was stopped is intentionally
not reconstructed in live mode.

Version 0 has no lossy queue between decoding and metric state. Source objects
are validated, applied to per-series state and SDK instruments, and then
committed to the watermark synchronously. If the reader falls behind, bytes
remain on disk until consumed or removed by upstream retention.

One periodic metric reader performs at most one bounded export at a time. There
is no application export queue. A failed intermediate export leaves cumulative
SDK state intact, so the next successful point includes additions from the
current adapter epoch. Final delivery is best effort within the shutdown
deadline. BlobFuse operation never depends on Collector availability.

BlobFuse and `bfusemon` can drop source updates before writing reports and
expose no dropped count. Exported totals are therefore best-effort lower bounds
for operational monitoring, not accounting data.

### Compatibility

Initially support only sanitized fixtures matching the inspected BlobFuse2
`2.5.6` / `bfusemon` `1.0.0-preview.1` implementation.

Each additional version requires:

- representative fixtures;
- parser and metric-mapping tests;
- rotation and abrupt-shutdown tests;
- an entry in a published compatibility matrix.

Unknown fields are tolerated. Incompatible known-field types fail visibly at
the field or record level.

## Consequences

### Positive

- The prototype works without replacing an official BlobFuse binary.
- It can be developed, released, and tested independently.
- OTLP provides one integration point for many observability backends.
- Failures cannot block BlobFuse's storage request path.
- Live state remains bounded and no state directory is required.
- The parser, normalization, metric mapping, and tests can later be reused by a
  compatible monitor or upstream implementation.
- Microsoft can evaluate a working metric contract rather than only a proposal.

### Negative

- Metrics are delayed by `bfusemon`'s three-timestamp memory buffer.
- Abrupt shutdown can lose observations not yet written to disk.
- Active-file parsing and rotation handling add complexity.
- The live adapter requires a dedicated per-mount directory and procfs process
  identity.
- Source-side queue loss is not detectable and can undercount every counter.
- The private source schema can change without notice.
- Operation latency histograms cannot be reconstructed from current reports.
- CPU and memory strings are dependent on Linux `top` behavior.

### Neutral

- A Collector becomes the recommended deployment companion.
- The first release is Linux-only, matching BlobFuse's runtime environment.
- Metrics begin at adapter attachment instead of reconstructing complete mount
  history.

## Alternatives Considered

### Modify `bfusemon` First

Add an exporter interface and OTLP exporter directly to BlobFuse.

This is the preferred long-term architecture, but it makes the prototype depend
on upstream approval and release timing. It also places new dependencies and
failure modes inside a Microsoft-supported companion binary before the metric
contract is validated.

### Replace `bfusemon`

Implement its CLI and private FIFO protocol in a compatible executable.

This removes report parsing and reduces latency, but the FIFO schema and startup
behavior are undocumented and unversioned. A replacement also assumes the name
and executable lookup behavior remain stable. Keep this as phase two if JSON
limitations become unacceptable.

### Fork BlobFuse

Refactor the health monitor and ship a custom BlobFuse distribution.

The MIT license permits this, but the fork would require continual security and
feature merges and would not carry the official binary's Microsoft support
status. Reserve this option for producer-side metrics that cannot be obtained by
any external boundary.

### Expose OpenMetrics Directly

Serve a Prometheus `/metrics` endpoint from the adapter.

This is operationally familiar, but it makes Prometheus the primary contract
and still requires another path for Azure Monitor and other OTLP backends. OTLP
plus a Collector is the more general first boundary.

### Use a Generic Log or File Agent

Configure an existing agent to tail the JSON files and map fields.

The active file is not valid JSON, rotation is stateful, cumulative counters
need reset-aware translation, and privacy requires a strict field allowlist. A
purpose-built source receiver is justified.

## Validation Criteria

The decision is validated when a proof of concept can:

1. consume active, unclosed, truncated, and rotated fixtures, including append
  and rotation during initialization;
2. reject stale PID history, identity changes, unsafe paths, and unsupported
  numeric values without exposing source contents;
3. restart by re-baselining retained reports without exporting historical
  increments;
4. emit the metric contract through OTLP/HTTP to a local Collector;
5. prove that monotonic sums use cumulative temporality and begin a new
  MeterProvider epoch after adapter restart;
6. recover cumulative additions after intermediate export failure and document
  the bounded final-delivery limitation;
7. deliver counters to Prometheus without delta-to-cumulative conversion;
8. satisfy the version 0 configuration, privacy, fixture, and resource-budget
  contracts.

## Revisit Triggers

Accepted post-v0 directions and their individual triggers are tracked in
[Post-v0 Follow-Up](../future-work.md).

Revisit this decision if:

- Microsoft accepts a stable native exporter or observation contract;
- JSON flush latency or data loss prevents useful monitoring;
- BlobFuse removes or materially changes health-monitor reports;
- users require latency distributions or block-cache telemetry unavailable in
  JSON;
- a required backend cannot consume cumulative OTLP or use Collector-based
  cumulative-to-delta conversion;
- OTLP/gRPC is required by a target deployment;
- direct Prometheus scraping is a demonstrated requirement rather than a
  preference.