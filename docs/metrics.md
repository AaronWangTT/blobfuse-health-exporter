# BlobFuse OTLP Metrics Contract

Status: Draft v0

## Purpose

This document defines the first supported translation from `bfusemon` reports
to OpenTelemetry metrics. It intentionally exposes a small aggregate surface.

The metric contract follows these OpenTelemetry rules:

- use lowercase dot-delimited namespaces;
- use UCUM units such as `By`, `s`, `1`, and `{operation}`;
- do not encode units in metric names;
- do not append `_total` to counters;
- use only bounded, documented attribute values;
- ensure aggregation across every attribute remains meaningful.

The `azure.blobfuse` namespace is project-specific and is not an official
OpenTelemetry semantic convention.

## Signal Scope

Version 0 exports metrics only.

Version 0 is a transport and metric-contract prototype over the existing
private JSON source. It is not intended to match the native telemetry depth of
Mountpoint for Amazon S3 or Cloud Storage FUSE.

It does not export:

- file or blob paths;
- upload/download progress for individual files;
- rename source or destination values;
- arbitrary error strings;
- one metric point per filesystem event;
- logs or traces.

Path-bearing source events are decoded and acknowledged so the live read
watermark can advance, then discarded. A future logs signal can be designed
separately with explicit privacy controls.

## Capability Gaps and Upstream Targets

The current JSON source cannot provide several signals that AWS Mountpoint and
Cloud Storage FUSE treat as core operational telemetry.

| Operator question | Current BlobFuse JSON | Version 0 | Upstream instrumentation target |
| --- | --- | --- | --- |
| How many filesystem operations occur? | Cumulative `libfuse` counters for a subset of operations | Export an experimental filesystem-operation counter | Typed counters at the complete FUSE operation boundary |
| Which filesystem operations fail? | No aggregate error counters or bounded error categories | Not available | Error counters by operation and bounded errno/error category |
| How long do filesystem operations take? | No latency distribution | Not available | FUSE operation latency histograms |
| How many Azure REST requests occur? | `azstorage` component method-call counters only | Not available | Request counters at the Azure SDK/HTTP boundary |
| Which Azure requests fail or retry? | No status, error, or retry aggregates | Not available | Request error/status counters and retry counters with bounded categories |
| How long do Azure requests take? | No request latency distribution | Not available | Total and first-byte request latency histograms |
| How much data reaches Azure Storage? | Cumulative uploaded/downloaded bytes | Export observed byte increments | Preserve as a typed producer counter |
| Is caching effective? | Whole-file download and served-from-cache counts; no block-cache metrics or latency | Export limited whole-file cache counters | File/block-cache hit, miss, byte, eviction, and latency metrics |

The first upstream proposal should prioritize request/error/retry counters and
FUSE/Azure request latency histograms. These cannot be reconstructed correctly
by an external JSON adapter.

## Resources and Scope

BlobFuse target metrics use a resource representing the monitored BlobFuse
process:

| Attribute | Requirement | Value |
| --- | --- | --- |
| `service.name` | Required | `blobfuse2` |
| `service.instance.id` | Required | Opaque SHA-256 ID derived from host boot ID, PID, and process start ticks |
| `process.pid` | Required for live sources | Monitored BlobFuse PID |
| `process.creation.time` | Required | Target process creation time in ISO 8601 |
| `service.version` | Optional | BlobFuse version, only when independently verified |
| `azure.blobfuse.monitor.source` | Required | `bfusemon_json` |

Compute `service.instance.id` as the lowercase hexadecimal SHA-256 digest of
the UTF-8 sequence `blobfuse-health-exporter/v0`, host boot ID, decimal PID, and
decimal procfs start ticks, separated by NUL bytes. Do not include report or
mount paths. The same BlobFuse process receives the same ID across adapter
restarts, while PID reuse and host reboot produce a different ID. Failure to
obtain any required input fails live-source startup.

Adapter self-metrics use a separate resource with `service.name` set to
`blobfuse-health-exporter`.

The instrumentation scope is:

```text
name: blobfuse-health-exporter
version: <adapter version>
```

Account names, container names, mount paths, cache paths, command lines, and
credentials are prohibited resource attributes.

## Bounded Metric Attributes

Only these metric attributes are initially permitted:

| Attribute | Allowed values |
| --- | --- |
| `azure.blobfuse.component.name` | `libfuse`, `azstorage` |
| `azure.blobfuse.io.direction` | `read`, `write` |
| `azure.blobfuse.operation.name` | Explicit operation allowlist below |

Operation values are normalized to lowercase snake case:

```text
create_dir
delete_dir
stream_dir
rename_dir
create_file
delete_file
rename_file
truncate_file
create_link
read_link
sync_file
sync_dir
chmod
```

Unknown source values must not be copied into attributes.

## Counter Translation

BlobFuse snapshots contain cumulative process-lifetime values, but the source
does not expose a reliable accumulation start timestamp. The adapter therefore
translates them through a stateful delta calculation performed independently
for each metric name and attribute set:

1. The first observed value becomes a baseline and emits no increment.
2. A larger value emits `current - previous` through an OpenTelemetry Counter.
3. An equal value emits nothing.
4. A smaller value is treated as a reset. It becomes the new baseline and emits
   no increment.
5. Duplicate source records are ignored before this calculation.

A series that has never been observed remains unknown and produces no point.
The adapter must not synthesize zero-valued baselines from missing keys.
Supported integer source values are limited by the exactness rules in the
[source contract](source-contract.md#numeric-precision).

The adapter must configure the OpenTelemetry SDK to export monotonic sums with
cumulative temporality. SDK defaults and environment configuration must not
silently change this contract.

Native delta temporality is intentionally unsupported in version 0. Its
conditional revisit triggers are documented in
[Post-v0 Follow-Up](future-work.md#delta-temporality-compatibility).

Source delta calculation and OTLP temporality are separate concerns. For source
values `100`, `130`, and `150`, the adapter establishes `100` as its baseline
and records additions of `30` and `20`; cumulative OTLP points report `30` and
then `50`. This avoids an attachment spike while retaining cumulative counter
semantics.

After baseline cutover, the adapter captures one epoch timestamp immediately
before constructing its MeterProvider. The SDK uses that provider lifecycle as
the start of the run's cumulative streams. A baseline without a later live
addition emits no zero counter point. The first live addition creates the first
point for that series. Adapter restart creates a new epoch even when the
monitored BlobFuse source session continues.

A source counter decrease within one adapter run changes only that source
series' translation baseline. It does not reset the adapter's cumulative OTLP
sum or MeterProvider epoch; subsequent positive source differences continue to
add to the observed adapter total.

Consequences:

- metric totals cover activity observed by the adapter, not the complete mount
  lifetime;
- a gap between adapter runs is not reconstructed;
- source resets are visible through an adapter self-metric;
- restart scans establish fresh per-series baselines and do not replay
  historical increments.

## Prometheus Compatibility

Cumulative monotonic OTLP sums match the Prometheus counter model and do not
require delta-to-cumulative conversion. Version 0 does not expose a Prometheus
scrape endpoint. A deployment must either enable Prometheus's OTLP receiver or
send OTLP to an OpenTelemetry Collector that exposes a Prometheus endpoint.

Integration tests must inspect exported OTLP payloads and assert cumulative
temporality, monotonic sums, expected accumulation across collection intervals,
no point for a baseline-only series, and a new provider start timestamp after
adapter restart. A Prometheus smoke test must ingest the counters without a
delta-to-cumulative processor.

## Accuracy and Delivery

BlobFuse component and monitor channels can discard updates before aggregation
or report writing and expose no dropped count. All BlobFuse-derived counters
are therefore best-effort lower bounds. They must not be used for billing,
auditing, or data-integrity accounting.

The adapter applies every accepted live object to metric state before advancing
its source watermark. It has no lossy record queue. Intermediate OTLP failures
do not discard cumulative SDK state; a later successful export includes all
adapter-observed additions in the current epoch. Final additions can still be
lost if the bounded shutdown flush fails.

## Source Timestamp Policy

Aggregate source timestamps represent the last component mutation, and objects
can remain buffered inside `bfusemon` for several intervals. Version 0 records
aggregate metric points at adapter observation time.

The original timestamp must not produce a source-age or lag health signal
because it remains old while a component is legitimately idle. It is not a
metric attribute. Backdated OTLP points can be reconsidered if a later direct
OTLP encoder preserves source timing without violating SDK semantics.

## Initial BlobFuse Metrics

### `azure.blobfuse.storage.io`

| Property | Value |
| --- | --- |
| Type | Counter |
| Unit | `By` |
| Description | Bytes transferred between BlobFuse and Azure Storage |
| Attributes | `azure.blobfuse.io.direction` |

Source mapping:

- `azstorage` / `Bytes Downloaded` -> `read`
- `azstorage` / `Bytes Uploaded` -> `write`

Only non-negative integer source values are accepted.

### `azure.blobfuse.fs.operations`

| Property | Value |
| --- | --- |
| Type | Counter |
| Unit | `{operation}` |
| Description | Filesystem operations observed at BlobFuse's libfuse boundary |
| Attributes | `azure.blobfuse.operation.name` |

Source mapping uses integer aggregate snapshot keys from `libfuse` that appear
in the explicit operation allowlist.

`azstorage` operation keys are deliberately excluded. They count calls into the
BlobFuse storage component and do not correspond one-to-one with Azure REST or
SDK requests. Presenting them as storage request telemetry would be misleading.

### `azure.blobfuse.file.open`

| Property | Value |
| --- | --- |
| Type | Gauge |
| Unit | `{file}` |
| Description | Current open file handles reported by a BlobFuse component |
| Attributes | `azure.blobfuse.component.name` |

Source mapping:

- `libfuse` / `OpenFileHandles`
- `azstorage` / `OpenFileHandles`

The two component values are separate observations and must not be summed by
the adapter.

### `azure.blobfuse.cache.file.downloads`

| Property | Value |
| --- | --- |
| Type | Counter |
| Unit | `{file}` |
| Description | Files downloaded into the whole-file cache |
| Attributes | None |

Source mapping: `file_cache` / `Files Downloaded`.

### `azure.blobfuse.cache.hits`

| Property | Value |
| --- | --- |
| Type | Counter |
| Unit | `{hit}` |
| Description | Open requests served from the whole-file cache |
| Attributes | None |

Source mapping: `file_cache` / `Files served from cache`.

This is not a complete block-level cache hit ratio and must not be described as
one.

### `azure.blobfuse.cache.usage`

| Property | Value |
| --- | --- |
| Type | Gauge |
| Unit | `By` |
| Description | Current disk usage reported by the whole-file cache policy |
| Attributes | None |

Source mapping: numeric prefix of `file_cache` / `Cache Usage`. The source uses
the text suffix `MB`; version 0 interprets it using BlobFuse's binary conversion
constant, where one unit is 1,048,576 bytes. Invalid, negative, NaN, or infinite
values are omitted.

### `azure.blobfuse.cache.utilization`

| Property | Value |
| --- | --- |
| Type | Gauge |
| Unit | `1` |
| Description | Fraction of the configured whole-file cache limit in use |
| Attributes | None |

Source mapping: numeric prefix of `file_cache` / `Usage Percent`, divided by
100. Values above `1` are retained because a configured limit can be exceeded.
Invalid, negative, NaN, or infinite values are omitted.

### `process.memory.virtual`

| Property | Value |
| --- | --- |
| Type | Observable UpDownCounter |
| Unit | `By` |
| Description | Committed virtual memory of the monitored BlobFuse process |
| Attributes | None |

Source mapping: top-level `MemoryUsage`, which is populated from Linux `top`'s
`VIRT` column. Known suffixes are converted to bytes. Unknown formats are
omitted and counted as parse errors.

This metric is intentionally not `process.memory.usage`, which means physical
memory in the OpenTelemetry process conventions.

## Deferred Metrics

### CPU

Top-level `CPUUsage` is Linux `top`'s `%CPU`. It is not divided by the number of
CPUs available to the process and does not include a CPU mode. It therefore does
not satisfy the definition of standard `process.cpu.utilization`.

Version 0 does not export it. A later version should read process CPU time and
CPU availability directly, then emit `process.cpu.time` and optionally derive
`process.cpu.utilization`.

### Network

`NetworkUsage` exists in the output structure, but the network monitor is not
registered and does not collect values. No network metric is defined.

### File-Cache Watcher Events

`FileCache.cacheSize`, `cacheFilesCount`, and `evictedFilesCount` are based on
event-local maps that start empty and do not represent authoritative cumulative
state. They are not exported in version 0.

### Latency Histograms

The current report contains no operation latency distribution. OTLP support
cannot reconstruct one from operation counts. FUSE and Azure request latency
histograms require upstream instrumentation or a compatible replacement
monitor.

### Azure Storage Component Calls

The source contains cumulative operation keys under `azstorage`, but these are
component method invocations rather than Azure REST requests. Version 0 does not
export them. A future diagnostic metric could expose them under an explicitly
experimental component-call name if an operator use case justifies it.

## Adapter Self-Metrics

These metrics describe the adapter, not BlobFuse:

| Metric | Type | Unit | Bounded attributes |
| --- | --- | --- | --- |
| `blobfuse_health_exporter.source.records` | Counter | `{record}` | `outcome`: `accepted`, `ignored`, `invalid` |
| `blobfuse_health_exporter.source.rotations` | Counter | `{rotation}` | None |
| `blobfuse_health_exporter.source.discontinuities` | Counter | `{discontinuity}` | `reason`: fixed allowlist |
| `blobfuse_health_exporter.source.counter_resets` | Counter | `{reset}` | `source.metric`: supported metric allowlist |
| `blobfuse_health_exporter.export.errors` | Counter | `{error}` | `error.type`: fixed allowlist |

Raw errors, paths, filenames, and payload fragments are prohibited attributes.

The version 0 `reason` values are `generation_missing`,
`generation_truncated`, `oversize_record`, `stale_generation`, and
`unclean_close`. The version 0 `error.type` values are `timeout`, `transport`,
and `shutdown`. New values require a metric-contract review.

## Missing and Invalid Values

- Missing aggregate key: retain the last known value.
- Missing optional top-level source: no update.
- Unknown source field: ignore and increment `outcome=ignored`.
- Incompatible known-field type: omit and increment `outcome=invalid`.
- Integer source value that is fractional, negative, greater than `2^53`, or
  outside `int64`: omit and increment `outcome=invalid`.
- Formatted number that cannot be converted: omit and increment a parse error.
- Counter decrease: reset baseline and increment `source.counter_resets`.
- Missing gauge update while the same source session is active: retain the last
  known value because unchanged component snapshots are intentionally omitted.
- Source session ends or is replaced: remove its gauge observations rather than
  carrying them into the new session.

## Privacy and Cardinality Tests

Automated tests must verify that no exported resource or data-point attribute
contains:

- report paths;
- mount or cache paths;
- account or container names;
- blob or file names;
- rename source or destination values;
- command lines;
- arbitrary source keys or error strings.

Tests must also assert the complete allowed attribute set and maximum number of
timeseries generated by every fixture.

## Version 0 Decisions

- Counter totals begin at the adapter's live cutover; absolute pre-attachment
  BlobFuse totals are not exported.
- Only allowlisted `libfuse` operations are exported. `azstorage` method calls
  are not represented as storage requests.
- `azure.blobfuse` is the project-specific namespace for this experimental
  contract.
- `process.memory.virtual` remains included with distribution compatibility
  fixtures and strict parsing.
- Only the explicitly defined whole-file cache metrics justify formatted-value
  parsing in version 0.

See [source-contract.md](source-contract.md) for the input limitations behind
these decisions.