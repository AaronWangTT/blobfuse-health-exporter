# BlobFuse Health Exporter: Version 0 Configuration

Status: Draft v0

## Purpose

This document defines the complete public configuration and operational
defaults for the version 0 proof of concept. Version 0 uses command-line flags
only. Unknown flags, invalid values, and missing required values fail before any
report is consumed.

Standard OpenTelemetry environment configuration and config files are deferred.
The implementation must configure the SDK explicitly so environment values
cannot silently change the endpoint, resource attributes, temporality, or
timeouts defined here.

## Command-Line Contract

| Option | Default | Version 0 rule |
| --- | --- | --- |
| `--report-dir` | None | Required absolute path to the dedicated, per-mount report directory |
| `--pid` | None | Required positive BlobFuse PID; process start identity must be readable from procfs |
| `--otlp-endpoint` | `http://127.0.0.1:4318/v1/metrics` | Full OTLP/HTTP metrics URL; version 0 accepts loopback HTTP only |
| `--rescan-interval` | `5s` | Interval for directory and process-identity reconciliation |
| `--initialization-timeout` | `30s` | Maximum time to reach the baseline-to-live cutover |
| `--source-drain-timeout` | `10s` | Maximum final drain after the source process exits or changes identity |
| `--export-interval` | `30s` | Periodic cumulative OTLP collection interval |
| `--export-timeout` | `10s` | Total deadline for one export attempt, including transport retry |
| `--shutdown-timeout` | `5s` | Deadline for final force-flush and SDK shutdown after source draining |
| `--max-record-bytes` | `16777216` | Maximum encoded size of one top-level JSON object |
| `--allow-insecure-source` | `false` | Development-only relaxation of source owner and mode checks |
| `--log-level` | `info` | One of `debug`, `info`, `warn`, or `error` |

Durations use Go duration syntax and must be positive. `--export-timeout` must
be less than `--export-interval`. The maximum record size must be between
1 MiB and 64 MiB. Configuration errors exit nonzero without opening reports or
contacting the OTLP endpoint.

The OTLP URL must use `http`, contain a loopback IP address or `localhost`, and
contain no user information, query, or fragment. Its path must end in
`/v1/metrics`.

The insecure-source override never permits symlinks, non-regular report files,
pathname-only validation, or an unreadable process identity. Logs must not
print the report path or OTLP URL query data at any level.

## Processing and Queue Model

Version 0 has no lossy record queue between source decoding and metric state.
The reader performs these steps synchronously for each complete object:

1. validate and normalize supported fields;
2. update per-series source state and OpenTelemetry instruments; and
3. advance the descriptor watermark.

If processing falls behind, unread bytes remain in the report files. Retention
rotation can still remove an undiscovered generation; that condition is
reported as a source discontinuity.

Metric collection and export run independently through one periodic reader.
At most one export request may be in flight. There is no application export
queue and no queue-capacity option. An export failure does not roll back or
discard cumulative SDK state; a later successful cumulative point includes
increments recorded since the current adapter epoch began.

The OTLP exporter may retry only within the current `--export-timeout` context.
After that attempt fails, the next attempt occurs at the next export interval.
There is no unbounded background retry loop.

## Lifecycle

Startup proceeds in this order:

1. parse and validate configuration;
2. validate the report directory and live process identity;
3. register file notifications and complete baseline initialization;
4. capture the adapter epoch timestamp and create the MeterProvider; and
5. publish baseline gauges and enter live processing.

If initialization cannot reach a live cutover before its timeout, the process
exits nonzero and exports no BlobFuse counters.

When the source process exits or its start identity changes, the adapter drains
the source for `--source-drain-timeout`. It then attempts one force-flush and SDK
shutdown bounded by `--shutdown-timeout`, removes source gauges, and exits. A
source identity change is not followed automatically.

External termination skips the source-drain period but still attempts the
bounded force-flush and SDK shutdown. Failure to flush is logged without
sensitive payload data; version 0 does not claim final-point delivery.

## Delivery Semantics

- BlobFuse reports are a best-effort source and can omit updates before they
  reach disk.
- Initialization establishes baselines and exports no historical increments.
- Activity while the adapter is stopped is not reconstructed.
- Every complete live source object accepted by the adapter reaches metric
  state before its input watermark advances.
- Failed intermediate cumulative exports are recoverable through a later
  successful point while the adapter remains running.
- Activity not flushed before final adapter shutdown can be lost.

These metrics are suitable for operational monitoring, not billing, auditing,
or data-integrity accounting.

## Resource Budgets

The initial acceptance budgets on a supported Linux x86-64 host are:

- idle median CPU below 1% of one core over five minutes;
- idle resident memory below 64 MiB;
- CPU below 10% of one core and resident memory below 128 MiB while consuming a
  sustained 1 MiB/s report stream with regular rotation; and
- bounded memory during Collector outage and malformed maximum-size records.

Benchmarks must report host, kernel, Go version, fixture rate, and collection
interval. A budget miss blocks the v0 milestone or requires this contract to be
revised explicitly.

## Configuration Tests

Tests must cover:

- every default and validation boundary;
- missing PID or report directory;
- non-loopback and malformed OTLP endpoints;
- export timeout greater than or equal to the export interval;
- maximum record sizes below, at, and above allowed limits;
- insecure-source override boundaries;
- initialization, source-drain, export, and shutdown deadlines; and
- proof that OpenTelemetry environment variables cannot alter the v0 contract.