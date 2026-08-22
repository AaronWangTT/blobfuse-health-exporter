#!/usr/bin/env bash

set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
blobfuse_repo=${BLOBFUSE2_REPO:-}
go_bin=${GO_BIN:-go}
blobfuse_go_bin=${BLOBFUSE_GO_BIN:-$go_bin}
otelcol_bin=${OTELCOL_BIN:-otelcol}
prometheus_bin=${PROMETHEUS_BIN:-prometheus}
azurite_bin=${AZURITE_BIN:-azurite-blob}
az_bin=${AZ_BIN:-az}
python_bin=${PYTHON_BIN:-python3}
keep_work_dir=${E2E_KEEP_WORK_DIR:-false}
work_dir=${E2E_WORK_DIR:-}
evidence_dir=${E2E_EVIDENCE_DIR:-}
stress_mode=${E2E_STRESS_MODE:-quick}
stress_timeout=${E2E_STRESS_TIMEOUT:-}
cache_size_mb=${E2E_CACHE_SIZE_MB:-64}
cache_timeout_sec=${E2E_CACHE_TIMEOUT_SEC:-120}
artifact_name=${E2E_ARTIFACT_NAME:-blobfuse-real-mount-metrics}
export_interval=${E2E_EXPORT_INTERVAL:-500ms}

azurite_pid=
prometheus_pid=
collector_pid=
blobfuse_pid=
bfusemon_pid=
exporter_pid=
stress_pid=
mounted=false

umask 0077

if [[ -z "$work_dir" ]]; then
    work_dir=$(mktemp -d)
else
    if [[ -e "$work_dir" && -n "$(find "$work_dir" -mindepth 1 -maxdepth 1 -print -quit 2>/dev/null)" ]]; then
        printf 'E2E_WORK_DIR must be absent or empty: %s\n' "$work_dir" >&2
        exit 2
    fi
    mkdir -p -- "$work_dir"
fi
work_dir=$(cd -- "$work_dir" && pwd)
chmod 0700 "$work_dir"

if [[ -z "$evidence_dir" ]]; then
    evidence_dir="$work_dir/evidence"
else
    if [[ -e "$evidence_dir" && -n "$(find "$evidence_dir" -mindepth 1 -maxdepth 1 -print -quit 2>/dev/null)" ]]; then
        printf 'E2E_EVIDENCE_DIR must be absent or empty: %s\n' "$evidence_dir" >&2
        exit 2
    fi
fi
mkdir -p -- "$evidence_dir"
evidence_dir=$(cd -- "$evidence_dir" && pwd)
chmod 0700 "$evidence_dir"

bin_dir="$work_dir/bin"
log_dir="$work_dir/logs"
report_dir="$work_dir/reports"
mount_dir="$work_dir/mount"
cache_dir="$work_dir/cache"
prometheus_data_dir="$work_dir/prometheus-data"
azurite_data_dir="$work_dir/azurite-data"
query_file="$work_dir/query.json"
config_file="$work_dir/blobfuse.yaml"
metrics_file="$evidence_dir/prometheus-metrics.json"
otlp_file="$evidence_dir/otel-metrics.log"
summary_file="$evidence_dir/summary.md"

mkdir -m 0700 \
    "$bin_dir" \
    "$log_dir" \
    "$report_dir" \
    "$mount_dir" \
    "$cache_dir" \
    "$prometheus_data_dir" \
    "$azurite_data_dir"

print_diagnostics() {
    printf '%s\n' '--- process state ---' >&2
    for entry in \
        "azurite:$azurite_pid" \
        "prometheus:$prometheus_pid" \
        "collector:$collector_pid" \
        "blobfuse2:$blobfuse_pid" \
        "bfusemon:$bfusemon_pid" \
        "exporter:$exporter_pid"; do
        local name=${entry%%:*}
        local process_id=${entry#*:}
        if [[ -n "$process_id" ]] && kill -0 "$process_id" 2>/dev/null; then
            printf '%s pid=%s running\n' "$name" "$process_id" >&2
        elif [[ -n "$process_id" ]]; then
            printf '%s pid=%s stopped\n' "$name" "$process_id" >&2
        fi
    done

    if [[ -d "$report_dir" ]]; then
        printf '%s\n' '--- report metadata (contents suppressed) ---' >&2
        find "$report_dir" -maxdepth 1 -type f \
            -printf '%f mode=%m uid=%U size=%s bytes\n' >&2 2>/dev/null || true
    fi

    for log_name in exporter collector prometheus blobfuse-stress blobfuse azurite; do
        printf '%s\n' "--- $log_name log ---" >&2
        tail -n 80 "$log_dir/$log_name.log" >&2 2>/dev/null || true
    done
}

write_failure_summary() {
    [[ -s "$summary_file" ]] && return

    cat >"$summary_file" <<'MARKDOWN'
# Azurite real-mount OTLP metrics

| Result | Details |
| --- | --- |
| **Failed** | The E2E did not reach sanitized metric evidence generation. See the workflow job log for diagnostics. |

No raw Blobfuse configuration or `bfusemon` report is included in the artifact.
MARKDOWN
}

terminate_process() {
    local process_id=$1

    if [[ -n "$process_id" ]] && kill -0 "$process_id" 2>/dev/null; then
        kill -TERM "$process_id" 2>/dev/null || true
    fi
}

terminate_process_group() {
    local process_id=$1

    if [[ -n "$process_id" ]] && kill -0 "$process_id" 2>/dev/null; then
        kill -TERM -- "-$process_id" 2>/dev/null || true
    fi
}

wait_for_process_exit() {
    local process_id=$1
    local timeout_seconds=$2
    local deadline=$((SECONDS + timeout_seconds))

    while kill -0 "$process_id" 2>/dev/null; do
        if ((SECONDS >= deadline)); then
            return 1
        fi
        sleep 0.2
    done
}

cleanup() {
    local status=$?
    set +e

    terminate_process_group "$stress_pid"
    if [[ -n "$stress_pid" ]]; then
        if ! wait_for_process_exit "$stress_pid" 5; then
            kill -KILL -- "-$stress_pid" 2>/dev/null || true
        fi
        wait "$stress_pid" 2>/dev/null || true
        stress_pid=
    fi

    if [[ "$mounted" == true ]] && mountpoint -q "$mount_dir"; then
        fusermount3 -u "$mount_dir" >/dev/null 2>&1 ||
            fusermount3 -uz "$mount_dir" >/dev/null 2>&1
    fi

    terminate_process "$blobfuse_pid"
    terminate_process "$exporter_pid"
    terminate_process "$bfusemon_pid"
    terminate_process "$collector_pid"
    terminate_process "$prometheus_pid"
    terminate_process "$azurite_pid"

    if [[ -n "$bfusemon_pid" ]] && ! wait_for_process_exit "$bfusemon_pid" 5; then
        kill -KILL "$bfusemon_pid" 2>/dev/null || true
        wait_for_process_exit "$bfusemon_pid" 5 || true
    fi

    for process_id in \
        "$blobfuse_pid" \
        "$exporter_pid" \
        "$collector_pid" \
        "$prometheus_pid" \
        "$azurite_pid"; do
        if [[ -n "$process_id" ]]; then
            wait "$process_id" 2>/dev/null || true
        fi
    done

    if ((status != 0)); then
        write_failure_summary
        print_diagnostics
    fi

    if [[ "$keep_work_dir" == true || ( -n "${E2E_WORK_DIR:-}" && $status -ne 0 ) ]]; then
        printf 'E2E work directory retained at %s\n' "$work_dir" >&2
    else
        rm -rf -- "$work_dir"
    fi

    exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

fail() {
    printf 'Azurite mount E2E failed: %s\n' "$1" >&2
    exit 1
}

case "$stress_mode" in
    quick)
        stress_quick=true
        stress_timeout=${stress_timeout:-5m}
        # Stress root + 3 phase roots + 8 leaf directories.
        expected_create_dirs=12
        expected_delete_dirs=12
        # 20 small + 2 big + 2 huge files.
        expected_delete_files=24
        ;;
    full)
        stress_quick=false
        stress_timeout=${stress_timeout:-120m}
        # Stress root + 3 phase roots + 62 leaf directories.
        expected_create_dirs=66
        expected_delete_dirs=66
        # 2,000 small + 20 big + 2 huge files.
        expected_delete_files=2022
        ;;
    *)
        fail "E2E_STRESS_MODE must be quick or full"
        ;;
esac

[[ "$cache_size_mb" =~ ^[1-9][0-9]*$ ]] ||
    fail "E2E_CACHE_SIZE_MB must be a positive integer"
[[ "$cache_timeout_sec" =~ ^[1-9][0-9]*$ ]] ||
    fail "E2E_CACHE_TIMEOUT_SEC must be a positive integer"

require_command() {
    local command_name=$1

    command -v "$command_name" >/dev/null 2>&1 ||
        fail "required command not found: $command_name"
}

allocate_port() {
    "$python_bin" -c \
        'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()'
}

wait_for_http() {
    local url=$1
    local process_id=$2
    local timeout_seconds=$3
    local deadline=$((SECONDS + timeout_seconds))

    while ((SECONDS < deadline)); do
        if curl --silent --output /dev/null "$url"; then
            return 0
        fi
        kill -0 "$process_id" 2>/dev/null || return 1
        sleep 0.2
    done
    return 1
}

wait_for_mount() {
    local timeout_seconds=$1
    local deadline=$((SECONDS + timeout_seconds))

    while ((SECONDS < deadline)); do
        if mountpoint -q "$mount_dir"; then
            return 0
        fi
        kill -0 "$blobfuse_pid" 2>/dev/null || return 1
        sleep 0.2
    done
    return 1
}

wait_for_file_pattern() {
    local file=$1
    local pattern=$2
    local process_id=$3
    local timeout_seconds=$4
    local deadline=$((SECONDS + timeout_seconds))

    while ((SECONDS < deadline)); do
        if [[ -f "$file" ]] && grep -F --quiet -- "$pattern" "$file"; then
            return 0
        fi
        kill -0 "$process_id" 2>/dev/null || return 1
        sleep 0.2
    done
    return 1
}

wait_for_query() {
    local base_url=$1
    local query=$2
    local timeout_seconds=$3
    local deadline=$((SECONDS + timeout_seconds))

    while ((SECONDS < deadline)); do
        if curl --fail --silent --show-error --get "$base_url/api/v1/query" \
            --data-urlencode "query=$query" >"$query_file" &&
            grep -F --quiet '"result":[{' "$query_file"; then
            return 0
        fi
        sleep 0.2
    done
    return 1
}

run_blobfuse_stress() {
    local stress_status

    printf 'Running Blobfuse %s stress workload...\n' "$stress_mode"
    (
        cd "$blobfuse_repo"
        exec setsid env GOTOOLCHAIN=local "$blobfuse_go_bin" test \
            -count=1 \
            -timeout="$stress_timeout" \
            -v test/stress_test/stress_test.go \
            -args \
            -mnt-path="$mount_dir" \
            -quick="$stress_quick"
    ) >"$log_dir/blobfuse-stress.log" 2>&1 &
    stress_pid=$!
    if wait "$stress_pid"; then
        stress_status=0
    else
        stress_status=$?
    fi
    stress_pid=
    return "$stress_status"
}

[[ -n "$blobfuse_repo" ]] || fail "BLOBFUSE2_REPO is required"
[[ "$blobfuse_repo" = /* ]] || fail "BLOBFUSE2_REPO must be an absolute path"
[[ -f "$blobfuse_repo/go.mod" ]] || fail "BLOBFUSE2_REPO does not contain go.mod"
[[ -d "$blobfuse_repo/tools/health-monitor" ]] ||
    fail "BLOBFUSE2_REPO does not contain tools/health-monitor"

for command_name in \
    "$go_bin" \
    "$blobfuse_go_bin" \
    "$otelcol_bin" \
    "$prometheus_bin" \
    "$azurite_bin" \
    "$az_bin" \
    "$python_bin" \
    curl \
    find \
    fusermount3 \
    grep \
    mountpoint \
    pgrep \
    setsid \
    stat; do
    require_command "$command_name"
done

[[ -c /dev/fuse ]] || fail "/dev/fuse is unavailable"
[[ -r /dev/fuse && -w /dev/fuse ]] || fail "/dev/fuse is not accessible to uid $(id -u)"

printf '%s\n' 'Building exporter, Blobfuse2, and bfusemon...'
(cd "$repo_root" && "$go_bin" build -trimpath -o "$bin_dir/blobfuse-health-exporter" .)
(
    cd "$blobfuse_repo"
    CGO_ENABLED=1 GOTOOLCHAIN=local "$blobfuse_go_bin" build -trimpath -o "$bin_dir/blobfuse2" .
    GOTOOLCHAIN=local "$blobfuse_go_bin" build -trimpath -o "$bin_dir/bfusemon" ./tools/health-monitor/
)

azurite_port=${AZURITE_BLOB_PORT:-$(allocate_port)}
prometheus_port=${PROMETHEUS_PORT:-$(allocate_port)}
collector_port=${OTELCOL_HTTP_PORT:-$(allocate_port)}
[[ "$azurite_port" != "$prometheus_port" ]] || fail "Azurite and Prometheus ports must differ"
[[ "$azurite_port" != "$collector_port" ]] || fail "Azurite and Collector ports must differ"
[[ "$prometheus_port" != "$collector_port" ]] || fail "Prometheus and Collector ports must differ"
prometheus_url="http://127.0.0.1:$prometheus_port"
collector_url="http://127.0.0.1:$collector_port"
azurite_url="http://127.0.0.1:$azurite_port"

printf 'Starting Azurite Blob service on %s...\n' "$azurite_url"
"$azurite_bin" \
    --silent \
    --skipApiVersionCheck \
    --location "$azurite_data_dir" \
    --blobHost 127.0.0.1 \
    --blobPort "$azurite_port" \
    >"$log_dir/azurite.log" 2>&1 &
azurite_pid=$!
wait_for_http "$azurite_url/devstoreaccount1" "$azurite_pid" 20 ||
    fail "Azurite did not become ready"

container_name="bfe2e$(date +%s)$RANDOM"
azurite_account_key='Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw=='
azurite_connection_string="DefaultEndpointsProtocol=http;AccountName=devstoreaccount1;AccountKey=$azurite_account_key;BlobEndpoint=$azurite_url/devstoreaccount1;"
"$az_bin" storage container create \
    --name "$container_name" \
    --connection-string "$azurite_connection_string" \
    --only-show-errors \
    --output none

printf 'Starting Prometheus OTLP receiver on %s...\n' "$prometheus_url"
"$prometheus_bin" \
    --config.file="$repo_root/test/integration/prometheus-otlp.yaml" \
    --storage.tsdb.path="$prometheus_data_dir" \
    --web.listen-address="127.0.0.1:$prometheus_port" \
    --web.enable-otlp-receiver \
    >"$log_dir/prometheus.log" 2>&1 &
prometheus_pid=$!
wait_for_http "$prometheus_url/-/ready" "$prometheus_pid" 20 ||
    fail "Prometheus did not become ready"

printf 'Starting OpenTelemetry Collector on %s...\n' "$collector_url"
OTELCOL_HTTP_ENDPOINT="127.0.0.1:$collector_port" \
PROMETHEUS_OTLP_ENDPOINT="$prometheus_url/api/v1/otlp/v1/metrics" \
    "$otelcol_bin" \
    --config="file:$repo_root/test/integration/otelcol-prometheus.yaml" \
    >"$log_dir/collector.log" 2>&1 &
collector_pid=$!
wait_for_http "$collector_url/v1/metrics" "$collector_pid" 20 ||
    fail "OpenTelemetry Collector did not become ready"

printf -v cache_size_line '  max-size-mb: %s' "$cache_size_mb"
printf -v cache_timeout_line '  timeout-sec: %s' "$cache_timeout_sec"
cat >"$config_file" <<YAML
logging:
  level: log_warning
  file-path: "$log_dir/blobfuse-internal.log"
  type: base

components:
  - libfuse
  - file_cache
  - attr_cache
  - azstorage

libfuse:
  attribute-expiration-sec: 0
  entry-expiration-sec: 0
  negative-entry-expiration-sec: 0
  ignore-open-flags: true

file_cache:
  path: "$cache_dir"
$cache_timeout_line
$cache_size_line
  allow-non-empty-temp: true
  cleanup-on-start: true

attr_cache:
  timeout-sec: 0

azstorage:
  type: block
  endpoint: "$azurite_url/devstoreaccount1"
  use-http: true
  account-name: devstoreaccount1
  account-key: "$azurite_account_key"
  mode: key
  container: "$container_name"

health_monitor:
  enable-monitoring: true
  stats-poll-interval-sec: 1
  process-monitor-interval-sec: 1
  output-path: "$report_dir"
  monitor-disable-list:
    - network_profiler
    - file_cache_monitor
YAML
chmod 0600 "$config_file"

printf '%s\n' 'Starting foreground Blobfuse2 mount under umask 077...'
(
    umask 0077
    export PATH="$bin_dir:$PATH"
    exec "$bin_dir/blobfuse2" mount "$mount_dir" \
        --config-file="$config_file" \
        --foreground=true \
        --disable-version-check=true
) >"$log_dir/blobfuse.log" 2>&1 &
blobfuse_pid=$!

wait_for_mount 30 || fail "Blobfuse2 did not mount successfully"
mounted=true

report_file="$report_dir/monitor_${blobfuse_pid}.json"
wait_for_file_pattern "$report_file" '[' "$blobfuse_pid" 30 ||
    fail "bfusemon did not create the expected report"

bfusemon_pid=$(pgrep -P "$blobfuse_pid" -x bfusemon 2>/dev/null | head -n 1 || true)
[[ -n "$bfusemon_pid" ]] || fail "bfusemon is not a child of the Blobfuse2 process"

report_mode=$(stat -c '%a' "$report_file")
report_mode_value=$((8#$report_mode))
(( (report_mode_value & 077) == 0 )) ||
    fail "bfusemon report mode $report_mode grants group or other access"
[[ $(stat -c '%u' "$report_file") == "$(id -u)" ]] ||
    fail "bfusemon report owner does not match the exporter uid"

baseline_name="baseline-$RANDOM"
baseline_dir="$mount_dir/$baseline_name"
baseline_file="$baseline_dir/file.txt"
mkdir "$baseline_dir"
printf 'baseline\n' >"$baseline_file"
rm "$baseline_file"
rmdir "$baseline_dir"
for baseline_pattern in '"CreateDir":' '"DeleteFile":' '"DeleteDir":'; do
    wait_for_file_pattern "$report_file" "$baseline_pattern" "$blobfuse_pid" 45 ||
        fail "bfusemon did not flush the $baseline_pattern baseline"
done

printf '%s\n' 'Starting exporter after the real Blobfuse baseline...'
"$bin_dir/blobfuse-health-exporter" \
    --report-dir "$report_dir" \
    --pid "$blobfuse_pid" \
    --otlp-endpoint "$collector_url/v1/metrics" \
    --rescan-interval 200ms \
    --initialization-timeout 15s \
    --source-drain-timeout 8s \
    --export-interval "$export_interval" \
    --export-timeout 250ms \
    --shutdown-timeout 3s \
    >"$log_dir/exporter.log" 2>&1 &
exporter_pid=$!

memory_query="{__name__=~\"process_memory_virtual.*\",process_pid=\"$blobfuse_pid\"}"
wait_for_query "$prometheus_url" "$memory_query" 30 ||
    fail "the real bfusemon memory metric was not ingested"

run_blobfuse_stress || fail "Blobfuse $stress_mode stress workload failed"

operation_query="{__name__=~\"azure_blobfuse_fs_operations.*\",azure_blobfuse_operation_name=\"create_dir\",process_pid=\"$blobfuse_pid\"} >= $expected_create_dirs"
wait_for_query "$prometheus_url" "$operation_query" 60 ||
    fail "the minimum post-cutover CreateDir total was not ingested"
operation_response=$(<"$query_file")

delete_file_query="{__name__=~\"azure_blobfuse_fs_operations.*\",azure_blobfuse_operation_name=\"delete_file\",process_pid=\"$blobfuse_pid\"} >= $expected_delete_files"
wait_for_query "$prometheus_url" "$delete_file_query" 60 ||
    fail "the minimum post-cutover DeleteFile total was not ingested"

delete_dir_query="{__name__=~\"azure_blobfuse_fs_operations.*\",azure_blobfuse_operation_name=\"delete_dir\",process_pid=\"$blobfuse_pid\"} >= $expected_delete_dirs"
wait_for_query "$prometheus_url" "$delete_dir_query" 60 ||
    fail "the minimum post-cutover DeleteDir total was not ingested"

private_marker="private-e2e-$RANDOM"
printf 'privacy probe\n' >"$mount_dir/$private_marker.txt"
wait_for_file_pattern "$report_file" "$private_marker" "$blobfuse_pid" 30 ||
    fail "bfusemon did not flush the path privacy probe"

for pattern in \
    '"service_name":"blobfuse2"' \
    "\"process_pid\":\"$blobfuse_pid\"" \
    '"azure_blobfuse_operation_name":"create_dir"'; do
    grep -F --quiet -- "$pattern" <<<"$operation_response" ||
        fail "Prometheus result is missing expected label: $pattern"
done

series_response=$(curl --fail --silent --show-error --get "$prometheus_url/api/v1/series" \
    --data-urlencode 'match[]={__name__=~"azure_blobfuse_.*|process_memory_virtual.*"}')
if grep -F --quiet -- "$private_marker" <<<"$series_response" ||
    grep -F --quiet -- "$private_marker" \
        "$log_dir/exporter.log" \
        "$log_dir/collector.log" \
        "$log_dir/prometheus.log"; then
    fail "a private source path escaped into exporter or backend telemetry"
fi

printf '%s\n' 'Unmounting Blobfuse2 and verifying exporter source shutdown...'
fusermount3 -u "$mount_dir"
mounted=false
wait_for_process_exit "$blobfuse_pid" 20 || fail "Blobfuse2 did not stop after unmount"
set +e
wait "$blobfuse_pid"
blobfuse_status=$?
set -e
blobfuse_pid=
((blobfuse_status == 0)) || fail "Blobfuse2 exited with status $blobfuse_status"

wait_for_process_exit "$exporter_pid" 20 || fail "exporter did not stop after the Blobfuse2 source exited"
set +e
wait "$exporter_pid"
exporter_status=$?
set -e
exporter_pid=
((exporter_status == 0)) || fail "exporter exited with status $exporter_status"

wait_for_file_pattern "$log_dir/collector.log" "azure.blobfuse.fs.operations" "$collector_pid" 10 ||
    fail "Collector debug output is missing the CreateDir metric"

all_metrics_query='{__name__=~"azure_blobfuse_.*|process_memory_virtual.*|blobfuse_health_exporter_.*"}'
curl --fail --silent --show-error --get "$prometheus_url/api/v1/query" \
    --data-urlencode "query=$all_metrics_query" >"$metrics_file"
grep -F --quiet '"result":[{' "$metrics_file" ||
    fail "Prometheus returned no Blobfuse or exporter metrics for evidence"

terminate_process "$collector_pid"
wait "$collector_pid" 2>/dev/null || true
collector_pid=

for sensitive_value in \
    "$private_marker" \
    "$baseline_name" \
    "$work_dir" \
    "$mount_dir" \
    "$cache_dir" \
    "$report_dir" \
    "$config_file" \
    "$container_name" \
    "$azurite_url" \
    "devstoreaccount1" \
    "$azurite_connection_string" \
    "$azurite_account_key"; do
    if grep -F --quiet -- "$sensitive_value" "$metrics_file" "$log_dir/collector.log"; then
        fail "sensitive source or configuration data reached metric evidence"
    fi
done

cp -- "$log_dir/collector.log" "$otlp_file"
chmod 0600 "$metrics_file" "$otlp_file"

"$python_bin" - \
    "$metrics_file" \
    "$summary_file" \
    "$report_mode" \
    "$stress_mode" \
    "$artifact_name" \
    "$expected_create_dirs" \
    "$expected_delete_files" \
    "$expected_delete_dirs" <<'PYTHON'
import json
import sys

(
    metrics_path,
    summary_path,
    report_mode,
    stress_mode,
    artifact_name,
    expected_create_dirs,
    expected_delete_files,
    expected_delete_dirs,
) = sys.argv[1:]
with open(metrics_path, encoding="utf-8") as metrics_stream:
    payload = json.load(metrics_stream)

results = payload.get("data", {}).get("result", [])
results.sort(key=lambda item: (
    item.get("metric", {}).get("__name__", ""),
    sorted(item.get("metric", {}).items()),
))

with open(metrics_path, "w", encoding="utf-8") as metrics_stream:
    json.dump(payload, metrics_stream, indent=2, sort_keys=True)
    metrics_stream.write("\n")

def markdown(value):
    return str(value).replace("`", "&#96;").replace("|", "\\|").replace("\n", " ")

with open(summary_path, "w", encoding="utf-8") as summary:
    summary.write("# Azurite real-mount OTLP metrics\n\n")
    summary.write("## Assertions\n\n")
    summary.write("| Check | Result | Evidence |\n")
    summary.write("| --- | --- | --- |\n")
    summary.write(f"| Strict report permissions | Pass | Report mode `{markdown(report_mode)}` |\n")
    summary.write("| Real `bfusemon` memory metric | Pass | Ingested by Prometheus |\n")
    summary.write(f"| Blobfuse stress workload | Pass | Mode `{markdown(stress_mode)}` |\n")
    summary.write(f"| Post-baseline `CreateDir` counter | Pass | At least `{markdown(expected_create_dirs)}` |\n")
    summary.write(f"| Post-baseline `DeleteFile` counter | Pass | At least `{markdown(expected_delete_files)}` |\n")
    summary.write(f"| Post-baseline `DeleteDir` counter | Pass | At least `{markdown(expected_delete_dirs)}` |\n")
    summary.write("| Path and configuration privacy | Pass | Evidence scan found no protected values |\n")
    summary.write("| Source-driven shutdown | Pass | Blobfuse and exporter exited cleanly |\n\n")
    summary.write("## Prometheus-Ingested Metrics\n\n")
    summary.write("| Metric | Labels | Value |\n")
    summary.write("| --- | --- | ---: |\n")
    for result in results:
        labels = dict(result.get("metric", {}))
        name = labels.pop("__name__", "unknown")
        label_text = "<br>".join(
            f"`{markdown(key)}={markdown(value)}`"
            for key, value in sorted(labels.items())
        ) or "None"
        value = result.get("value", [None, "unknown"])[1]
        summary.write(f"| `{markdown(name)}` | {label_text} | `{markdown(value)}` |\n")
    summary.write("\n## Exact OTLP Structures\n\n")
    summary.write(
        f"The workflow artifact `{markdown(artifact_name)}` contains "
        "`otel-metrics.log`, the Collector's detailed debug representation, and "
        "`prometheus-metrics.json`, the corresponding query result. Raw `bfusemon` "
        "reports and Blobfuse configuration are excluded.\n"
    )
PYTHON
chmod 0600 "$summary_file"

printf '%s\n' 'Azurite real-mount E2E passed'