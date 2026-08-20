#!/usr/bin/env bash

set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
go_bin=${GO_BIN:-go}
prometheus_bin=${PROMETHEUS_BIN:-prometheus}
listen_address=${PROMETHEUS_LISTEN_ADDRESS:-127.0.0.1:19090}
base_url="http://$listen_address"
temp_dir=$(mktemp -d)
exporter_pid=
prometheus_pid=
target_pid=

cleanup() {
    set +e
    for pid in "$exporter_pid" "$prometheus_pid" "$target_pid"; do
        if [[ -n "$pid" ]]; then
            kill -TERM "$pid" 2>/dev/null
        fi
    done
    for pid in "$exporter_pid" "$prometheus_pid" "$target_pid"; do
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

wait_for_query() {
    local query=$1
    local timeout_seconds=$2
    local output_file=$3
    local deadline=$((SECONDS + timeout_seconds))

    while ((SECONDS < deadline)); do
        if curl -fsS --get "$base_url/api/v1/query" \
            --data-urlencode "query=$query" >"$output_file" &&
            grep -F '"result":[{' "$output_file" >/dev/null; then
            return 0
        fi
    done
    return 1
}

wait_for_exit() {
    local pid=$1
    local timeout_seconds=$2

    timeout "$timeout_seconds" tail --pid="$pid" -f /dev/null >/dev/null
}

print_diagnostics() {
    printf '%s\n' '--- exporter log ---' >&2
    tail -n 80 "$temp_dir/exporter.log" >&2 2>/dev/null || true
    printf '%s\n' '--- prometheus log ---' >&2
    tail -n 120 "$temp_dir/prometheus.log" >&2 2>/dev/null || true
    printf '%s\n' '--- last query response ---' >&2
    cat "$temp_dir/query.json" >&2 2>/dev/null || true
}

fail() {
    printf 'Prometheus smoke test failed: %s\n' "$1" >&2
    print_diagnostics
    exit 1
}

command -v "$go_bin" >/dev/null || fail "Go executable not found: $go_bin"
command -v "$prometheus_bin" >/dev/null || fail "Prometheus executable not found: $prometheus_bin"
command -v curl >/dev/null || fail "curl is required"

(cd "$repo_root" && "$go_bin" build -o "$temp_dir/blobfuse-health-exporter" .)

mkdir "$temp_dir/prometheus-data"
"$prometheus_bin" \
    --config.file="$repo_root/test/integration/prometheus-otlp.yaml" \
    --storage.tsdb.path="$temp_dir/prometheus-data" \
    --web.listen-address="$listen_address" \
    --web.enable-otlp-receiver \
    >"$temp_dir/prometheus.log" 2>&1 &
prometheus_pid=$!
wait_for_log "$temp_dir/prometheus.log" "Server is ready to receive web requests" 10s ||
    fail "Prometheus did not become ready"

tail -f /dev/null &
target_pid=$!
monitored_pid=$target_pid
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
    --otlp-endpoint "$base_url/api/v1/otlp/v1/metrics" \
    --rescan-interval 100ms \
    --initialization-timeout 5s \
    --source-drain-timeout 1s \
    --export-interval 500ms \
    --export-timeout 250ms \
    --shutdown-timeout 2s \
    >"$temp_dir/exporter.log" 2>&1 &
exporter_pid=$!

open_files_selector='{__name__=~"azure_blobfuse_file_open.*",azure_blobfuse_component_name="libfuse"}'
wait_for_query "$open_files_selector" 10 "$temp_dir/query.json" ||
    fail "baseline gauges were not ingested"

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

storage_query='{__name__=~"azure_blobfuse_storage_io.*",azure_blobfuse_io_direction="read"} == 37'
wait_for_query "$storage_query" 10 "$temp_dir/query.json" ||
    fail "Prometheus did not retain the cumulative read delta"
storage_response=$(<"$temp_dir/query.json")

open_files_query="$open_files_selector == 5"
wait_for_query "$open_files_query" 10 "$temp_dir/query.json" ||
    fail "Prometheus did not retain the live open-file gauge"

self_query='blobfuse_health_exporter_source_records_total{outcome="accepted",service_name="blobfuse-health-exporter"} == 2'
wait_for_query "$self_query" 10 "$temp_dir/query.json" ||
    fail "Prometheus did not ingest adapter self-metrics"
self_response=$(<"$temp_dir/query.json")
if grep -F '"process_pid":' <<<"$self_response" >/dev/null; then
    fail "adapter self-metrics retained the target process PID"
fi

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

series_response=$(curl -fsS --get "$base_url/api/v1/series" \
    --data-urlencode 'match[]={__name__=~"azure_blobfuse_.*|process_memory_virtual.*"}')

for pattern in \
    '"service_name":"blobfuse2"' \
    '"service_instance_id":' \
    "\"process_pid\":\"$monitored_pid\"" \
    '"process_creation_time":' \
    '"azure_blobfuse_monitor_source":"bfusemon_json"'; do
    grep -F -- "$pattern" <<<"$storage_response" >/dev/null ||
        fail "Prometheus series is missing promoted resource: $pattern"
done

for marker in "private/smoke-secret" "smoke-secret-key"; do
    if grep -F -- "$marker" <<<"$series_response" >/dev/null ||
        grep -F -- "$marker" "$temp_dir/prometheus.log" "$temp_dir/exporter.log" >/dev/null; then
        fail "sensitive source marker escaped: $marker"
    fi
done

kill -TERM "$prometheus_pid"
wait "$prometheus_pid" 2>/dev/null || true
prometheus_pid=

printf '%s\n' "Prometheus smoke test passed"
printf '%s\n' "$storage_response"
printf '%s\n' "$self_response"