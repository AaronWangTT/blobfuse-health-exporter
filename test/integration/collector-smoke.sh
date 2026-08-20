#!/usr/bin/env bash

set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
go_bin=${GO_BIN:-go}
otelcol_bin=${OTELCOL_BIN:-otelcol}
temp_dir=$(mktemp -d)
collector_pid=
exporter_pid=
target_pid=

cleanup() {
    set +e
    for pid in "$exporter_pid" "$collector_pid" "$target_pid"; do
        if [[ -n "$pid" ]]; then
            kill -TERM "$pid" 2>/dev/null
        fi
    done
    for pid in "$exporter_pid" "$collector_pid" "$target_pid"; do
        if [[ -n "$pid" ]]; then
            wait "$pid" 2>/dev/null
        fi
    done
    rm -rf -- "$temp_dir"
}
trap cleanup EXIT INT TERM

wait_for_log() {
    local log_file=$1
    local pattern=$2
    local timeout_seconds=$3
    local pipeline_status

    set +o pipefail
    timeout "$timeout_seconds" tail -n +1 -F "$log_file" 2>/dev/null |
        grep -F -m 1 -- "$pattern" >/dev/null
    pipeline_status=("${PIPESTATUS[@]}")
    set -o pipefail
    return "${pipeline_status[1]}"
}

wait_for_exit() {
    local pid=$1
    local timeout_seconds=$2

    timeout "$timeout_seconds" tail --pid="$pid" -f /dev/null >/dev/null
}

print_diagnostics() {
    printf '%s\n' '--- exporter log ---' >&2
    tail -n 80 "$temp_dir/exporter.log" >&2 2>/dev/null || true
    printf '%s\n' '--- collector log ---' >&2
    tail -n 160 "$temp_dir/collector.log" >&2 2>/dev/null || true
}

fail() {
    printf 'collector smoke test failed: %s\n' "$1" >&2
    print_diagnostics
    exit 1
}

command -v "$go_bin" >/dev/null || fail "Go executable not found: $go_bin"
command -v "$otelcol_bin" >/dev/null || fail "Collector executable not found: $otelcol_bin"

(cd "$repo_root" && "$go_bin" build -o "$temp_dir/blobfuse-health-exporter" .)

"$otelcol_bin" \
    --config="file:$repo_root/test/integration/otelcol.yaml" \
    >"$temp_dir/collector.log" 2>&1 &
collector_pid=$!
wait_for_log "$temp_dir/collector.log" "Everything is ready" 10s ||
    fail "Collector did not become ready"

tail -f /dev/null &
target_pid=$!
report_dir="$temp_dir/reports"
report_file="$report_dir/monitor_${target_pid}.json"
mkdir -m 0700 "$report_dir"
umask 0077
cat >"$report_file" <<'JSON'
[
  {
    "BlobfuseStats": [
      {"componentName":"azstorage","value":{"Bytes Downloaded":100,"Bytes Uploaded":20}},
      {"componentName":"libfuse","value":{"CreateFile":5,"OpenFileHandles":3}},
      {"componentName":"file_cache","value":{"Files Downloaded":3,"Files served from cache":2,"Cache Usage":"10.000000 MB","Usage Percent":"25.00%"}},
      {"componentName":"azstorage","operation":"CreateFile","path":"private/smoke-secret","value":{"Src":"smoke-secret-key","Bytes Transferred":99}}
    ],
    "MemoryUsage":"100m"
  },
JSON

"$temp_dir/blobfuse-health-exporter" \
    --report-dir "$report_dir" \
    --pid "$target_pid" \
    --otlp-endpoint http://127.0.0.1:4318/v1/metrics \
    --rescan-interval 100ms \
    --initialization-timeout 5s \
    --source-drain-timeout 1s \
    --export-interval 500ms \
    --export-timeout 250ms \
    --shutdown-timeout 2s \
    >"$temp_dir/exporter.log" 2>&1 &
exporter_pid=$!
wait_for_log "$temp_dir/collector.log" "azure.blobfuse.file.open" 10s ||
    fail "baseline gauges were not exported"

cat >>"$report_file" <<'JSON'
  {
    "BlobfuseStats": [
      {"componentName":"azstorage","value":{"Bytes Downloaded":137,"Bytes Uploaded":31}},
      {"componentName":"libfuse","value":{"CreateFile":8,"OpenFileHandles":5}},
      {"componentName":"file_cache","value":{"Files Downloaded":5,"Files served from cache":4,"Cache Usage":"12.000000 MB","Usage Percent":"30.00%"}}
    ],
    "MemoryUsage":"120m"
  }
]
JSON

wait_for_log "$temp_dir/collector.log" "Value: 37" 10s ||
    fail "post-cutover cumulative delta was not exported"

kill -TERM "$target_pid"
wait "$target_pid" 2>/dev/null || true
target_pid=
wait_for_exit "$exporter_pid" 10s || fail "exporter did not stop after source exit"
set +e
wait "$exporter_pid"
exporter_status=$?
set -e
exporter_pid=
[[ $exporter_status -eq 0 ]] || fail "exporter exited with status $exporter_status"

kill -TERM "$collector_pid"
wait "$collector_pid" 2>/dev/null || true
collector_pid=

for pattern in \
    "azure.blobfuse.storage.io" \
    "azure.blobfuse.io.direction" \
    "azure.blobfuse.file.open" \
    "AggregationTemporality: Cumulative" \
    "IsMonotonic: true" \
    "Value: 37" \
    "service.name" \
    "service.instance.id" \
    "process.pid" \
    "process.creation.time" \
    "azure.blobfuse.monitor.source" \
    "blobfuse_health_exporter.source.records" \
    "service.name: Str(blobfuse-health-exporter)"; do
    grep -F -- "$pattern" "$temp_dir/collector.log" >/dev/null ||
        fail "Collector output is missing: $pattern"
done

for marker in "private/smoke-secret" "smoke-secret-key"; do
    if grep -F -- "$marker" "$temp_dir/collector.log" "$temp_dir/exporter.log" >/dev/null; then
        fail "sensitive source marker escaped: $marker"
    fi
done

printf '%s\n' "Collector smoke test passed"
grep -E \
    'Name: (azure\.blobfuse\.(storage\.io|file\.open)|blobfuse_health_exporter\.source\.records)|AggregationTemporality:|IsMonotonic:|Value: 37|service\.name: Str\(blobfuse-health-exporter\)|Key: (service\.name|service\.instance\.id|process\.pid|process\.creation\.time|azure\.blobfuse\.monitor\.source)' \
    "$temp_dir/collector.log" | tail -n 24