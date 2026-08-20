# BlobFuse Health Exporter: Post-v0 Follow-Up

Status: Accepted direction, deferred

## Purpose

This document records engineering directions accepted during peer review but
deliberately excluded from the version 0 proof of concept. They are not v0
requirements or compatibility promises.

The v0 milestone should first validate that the private `bfusemon` JSON source
can be consumed safely and translated into a useful, bounded OTLP metric set.
Each item below should receive a focused design review before implementation.

## Revisit Gate

Revisit these items after the v0 prototype has:

1. passed rotation, restart, privacy, and compatibility tests;
2. demonstrated useful metrics against a local OpenTelemetry Collector;
3. completed a Prometheus ingestion smoke test; and
4. produced enough operational feedback to distinguish required product
   behavior from speculative flexibility.

An item may be pulled forward when its individual trigger occurs earlier.

## Delta Temporality Compatibility

Cumulative temporality remains the sole version 0 contract and the preferred
long-term default. Native delta export is not planned unless a concrete
interoperability or resource requirement appears.

Prefer downstream cumulative-to-delta conversion in an OpenTelemetry Collector
when only one destination requires delta. Revisit native delta output if:

- a required backend rejects cumulative OTLP sums;
- a direct backend deployment cannot use Collector conversion;
- future histogram support demonstrates a backend-specific delta requirement;
   or
- measured cumulative aggregation state becomes material at the supported
   cardinality.

Any native delta option must define reset, lost-export, and adapter-restart
semantics and must pass integration tests against the requiring backend. It
must not silently replace cumulative output for existing deployments.

## Review Item 9: Declarative Metric Registry and Stability

**Accepted direction**

Introduce one declarative metric registry as the source of truth for:

- instrument names, kinds, units, and descriptions;
- source-field mappings and normalization rules;
- bounded attribute names and allowed values;
- stability classifications; and
- generated documentation and contract tests.

Use `stable`, `experimental`, and `internal` stability levels. Metrics derived
from the private JSON contract should initially be `experimental`; adapter
self-metrics may be `internal` until their operational contract is reviewed.

**v0 boundary**

The [v0 metrics contract](metrics.md) remains the manually maintained source of
truth. Its small metric set and strict allowlists keep drift manageable for the
proof of concept. No code generation pipeline is required for v0.

**Revisit when**

- the metric surface expands beyond the initial allowlist;
- a second source adapter or exporter needs the same definitions;
- documentation and implementation begin to drift; or
- the project prepares its first non-experimental metric commitment.

**Follow-up validation**

- CI proves generated code, documentation, and fixtures are current;
- duplicate names and invalid units fail generation;
- attributes without bounded allowlists fail validation; and
- stability changes require an explicit compatibility review.

## Review Item 10: Typed Linux Process Metrics

**Accepted direction**

Read typed process data from Linux procfs rather than parsing localized `top`
output. Candidate instruments include:

- `process.memory.usage` for resident physical memory;
- `process.memory.virtual` for virtual address space; and
- `process.cpu.time` with bounded CPU-mode attributes.

The implementation must bind reads to the BlobFuse source-session identity so
PID reuse cannot attach measurements to the wrong process. OpenTelemetry
semantic conventions in use at implementation time determine the final names
and instrument kinds.

**v0 boundary**

Version 0 exports only the existing `bfusemon` virtual-memory value after
strict parsing. It continues to omit CPU and physical-memory metrics. Its only
procfs access reads boot and process-start identity; it does not derive metric
values from procfs.

**Revisit when**

- operators require RSS or CPU metrics;
- `top` formatting causes compatibility failures on a supported distribution;
- container PID namespaces make the existing source unreliable; or
- a compatible monitor replacement already needs procfs access.

**Follow-up validation**

- fixtures cover supported kernels, page sizes, and PID namespaces;
- tests cover process exit and PID reuse during collection;
- byte and time-unit conversions are checked against typed procfs values; and
- permission failures degrade only process metrics, not BlobFuse metrics.

## Review Item 11: Direct Secure OTLP Configuration

**Accepted direction**

Keep the OpenTelemetry Collector as the recommended deployment, but do not make
it mandatory. Honor the standard metric-specific and generic OTLP environment
settings for:

- endpoint and protocol;
- headers;
- server and client certificates;
- timeout; and
- compression.

Define configuration precedence explicitly. TLS verification must remain the
default for remote endpoints, and headers, certificates, and credentials must
never be logged.

**v0 boundary**

Version 0 targets a trusted local OTLP/HTTP endpoint. A local Collector owns
remote authentication, TLS, enrichment, and routing. Direct remote-backend
security configuration is not required for the proof of concept.

**Revisit when**

- a deployment cannot run a Collector;
- direct Prometheus OTLP ingestion must cross a trust boundary;
- users require a direct managed-backend connection; or
- Collector operation outweighs the adapter's deployment cost.

**Follow-up validation**

- interoperability tests cover standard environment-variable precedence;
- TLS tests cover public CA, private CA, and mutual TLS endpoints;
- authentication headers are verified without appearing in logs; and
- insecure remote transport requires an explicit, visible opt-in.

## Review Item 12: Permanent Export Failure Classification

**Accepted direction**

Classify exporter failures using typed transport and protocol status data, not
substring matching. Separate at least:

- transient failures eligible for bounded backoff and retry;
- throttling responses that honor server retry guidance;
- permanent authentication, authorization, and configuration failures; and
- shutdown-time flush failures.

Permanent failures should stop repetitive export attempts until configuration
or process state changes. Logs must be rate limited, adapter self-metrics must
expose the state without sensitive details, and shutdown flush must have a
strict timeout.

**v0 boundary**

Version 0 permits at most one export in flight, bounds each attempt and shutdown
flush by the configured deadlines, and remains independent of Collector
availability. It does not promise complete typed failure classification or an
automatic disablement policy.

**Revisit when**

- direct secure OTLP export is implemented;
- production trials reveal persistent retry noise or resource waste;
- backend-specific status behavior must be normalized; or
- stronger final-delivery guarantees become a release requirement.

**Follow-up validation**

- table-driven tests cover typed HTTP and gRPC status classifications;
- transient backoff is bounded and respects cancellation;
- permanent failures do not create retry or log storms;
- credentials and response bodies are not exposed; and
- shutdown completes within its configured deadline.

## Proposed Follow-Up Order

1. Add the declarative registry before expanding or stabilizing the metric set.
2. Design direct OTLP security and failure classification together because
   retry policy depends on transport status and configuration.
3. Add procfs metrics independently when operator demand or portability data
   justifies the additional process-access boundary.

## Peer References

- [Mountpoint for Amazon S3 metrics](https://github.com/awslabs/mountpoint-s3/blob/main/doc/METRICS.md)
- [Cloud Storage FUSE metric registry](https://github.com/GoogleCloudPlatform/gcsfuse/blob/master/metrics/metrics.yaml)
- [Cloud Storage FUSE metrics](https://docs.cloud.google.com/storage/docs/cloud-storage-fuse/metrics)
- [Cloud Storage FUSE OTLP implementation](https://github.com/GoogleCloudPlatform/gcsfuse/pull/5017)