#!/usr/bin/env bash

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
go_bin=${GO_BIN:-go}
otelcol_bin=${OTELCOL_BIN:-otelcol}
measurement_duration=${MEASUREMENT_DURATION:-5m}
sample_interval=${SAMPLE_INTERVAL:-1s}
export_interval=${EXPORT_INTERVAL:-30s}
export_timeout=${EXPORT_TIMEOUT:-5s}
rescan_interval=${RESCAN_INTERVAL:-5s}
source_drain_timeout=${SOURCE_DRAIN_TIMEOUT:-10s}
ready_timeout=${READY_TIMEOUT:-45s}

temp_dir=
collector_pid=
exporter_pid=
target_pid=
writer_pid=
report_dir=
report_file=

budget_cleanup() {
    set +e
    if [[ -n "$temp_dir" ]]; then
        touch "$temp_dir/stop-writer" 2>/dev/null
    fi
    for pid in "$writer_pid" "$exporter_pid" "$collector_pid" "$target_pid"; do
        if [[ -n "$pid" ]]; then
            kill -TERM "$pid" 2>/dev/null
        fi
    done
    for pid in "$writer_pid" "$exporter_pid" "$collector_pid" "$target_pid"; do
        if [[ -n "$pid" ]]; then
            wait "$pid" 2>/dev/null
        fi
    done
    if [[ -n "$temp_dir" ]]; then
        rm -rf -- "$temp_dir"
    fi
}

budget_fail() {
    printf 'resource-budget scenario failed: %s\n' "$1" >&2
    if [[ -n "$temp_dir" ]]; then
        printf '%s\n' '--- exporter log ---' >&2
        tail -n 80 "$temp_dir/exporter.log" >&2 2>/dev/null || true
        printf '%s\n' '--- collector log ---' >&2
        tail -n 120 "$temp_dir/collector.log" >&2 2>/dev/null || true
    fi
    exit 1
}

budget_wait_for_log() {
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

budget_setup() {
    command -v "$go_bin" >/dev/null || budget_fail "Go executable not found: $go_bin"
    command -v "$otelcol_bin" >/dev/null || budget_fail "Collector executable not found: $otelcol_bin"
    temp_dir=$(mktemp -d)

    (cd "$repo_root" && "$go_bin" build -o "$temp_dir/blobfuse-health-exporter" .)
    (cd "$repo_root" && "$go_bin" build -o "$temp_dir/procstat" ./test/performance/procstat)

    "$otelcol_bin" \
        --config="file:$repo_root/test/performance/otelcol.yaml" \
        >"$temp_dir/collector.log" 2>&1 &
    collector_pid=$!
    budget_wait_for_log "$temp_dir/collector.log" "Everything is ready" 10s ||
        budget_fail "Collector did not become ready"

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
      {"componentName":"libfuse","value":{"OpenFileHandles":3}}
    ],
    "MemoryUsage":"100m"
  },
JSON
}

budget_start_exporter() {
    "$temp_dir/blobfuse-health-exporter" \
        --report-dir "$report_dir" \
        --pid "$target_pid" \
        --otlp-endpoint http://127.0.0.1:14318/v1/metrics \
        --rescan-interval "$rescan_interval" \
        --initialization-timeout 30s \
        --source-drain-timeout "$source_drain_timeout" \
        --export-interval "$export_interval" \
        --export-timeout "$export_timeout" \
        --shutdown-timeout 5s \
        >"$temp_dir/exporter.log" 2>&1 &
    exporter_pid=$!
    budget_wait_for_log "$temp_dir/collector.log" "azure.blobfuse.file.open" "$ready_timeout" ||
        budget_fail "exporter did not reach its first collection"
}

budget_measure() {
    local max_median_cpu=$1
    local max_average_cpu=$2
    local max_rss_bytes=$3

    "$temp_dir/procstat" \
        --pid "$exporter_pid" \
        --duration "$measurement_duration" \
        --interval "$sample_interval" \
        --max-median-cpu "$max_median_cpu" \
        --max-average-cpu "$max_average_cpu" \
        --max-rss-bytes "$max_rss_bytes"
}

budget_stop_source() {
    if [[ -n "$writer_pid" ]]; then
        touch "$temp_dir/stop-writer"
        timeout 5s tail --pid="$writer_pid" -f /dev/null >/dev/null ||
            budget_fail "load writer did not stop"
        wait "$writer_pid"
        writer_pid=
    fi
    kill -TERM "$target_pid"
    wait "$target_pid" 2>/dev/null || true
    target_pid=
    timeout 30s tail --pid="$exporter_pid" -f /dev/null >/dev/null ||
        budget_fail "exporter did not stop after source exit"
    set +e
    wait "$exporter_pid"
    local exporter_status=$?
    set -e
    exporter_pid=
    [[ $exporter_status -eq 0 ]] || budget_fail "exporter exited with status $exporter_status"
}

budget_print_environment() {
    local scenario=$1
    printf 'scenario=%s\n' "$scenario"
    printf 'kernel=%s\n' "$(uname -srmo)"
    printf 'distribution=%s\n' "$(. /etc/os-release && printf '%s' "$PRETTY_NAME")"
    printf 'cpu_model=%s\n' "$(sed -n 's/^model name[[:space:]]*: //p' /proc/cpuinfo | head -n 1)"
    printf 'logical_cpus=%s\n' "$(getconf _NPROCESSORS_ONLN)"
    printf 'memory_total_kib=%s\n' "$(awk '/^MemTotal:/ {print $2}' /proc/meminfo)"
    printf 'go=%s\n' "$("$go_bin" version)"
    printf 'collector=%s\n' "$("$otelcol_bin" --version 2>&1 | head -n 1)"
    printf 'measurement_duration=%s\n' "$measurement_duration"
    printf 'sample_interval=%s\n' "$sample_interval"
    printf 'export_interval=%s\n' "$export_interval"
    printf 'rescan_interval=%s\n' "$rescan_interval"
}