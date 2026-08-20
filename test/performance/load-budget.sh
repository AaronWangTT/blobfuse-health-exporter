#!/usr/bin/env bash

set -euo pipefail

source "$(dirname -- "${BASH_SOURCE[0]}")/common.sh"
trap budget_cleanup EXIT INT TERM

load_bytes_per_second=${LOAD_BYTES_PER_SECOND:-1048576}
rotate_every_records=${ROTATE_EVERY_RECORDS:-10}
max_average_cpu=${MAX_AVERAGE_CPU_PERCENT:-10}
max_rss_bytes=${MAX_RSS_BYTES:-134217728}

if [[ ! "$load_bytes_per_second" =~ ^[1-9][0-9]*$ ]]; then
    budget_fail "LOAD_BYTES_PER_SECOND must be a positive integer"
fi
if [[ ! "$rotate_every_records" =~ ^[1-9][0-9]*$ ]]; then
    budget_fail "ROTATE_EVERY_RECORDS must be a positive integer"
fi

start_load_writer() {
    local padding_file="$temp_dir/padding"
    head -c "$load_bytes_per_second" /dev/zero | tr '\0' x >"$padding_file"

    (
        set -euo pipefail
        local_counter=100
        local_records=0
        report_base="$report_dir/monitor_${target_pid}"
        while [[ ! -e "$temp_dir/stop-writer" ]]; do
            local_counter=$((local_counter + 1))
            {
                printf '{"BlobfuseStats":[{"componentName":"azstorage","value":{"Bytes Downloaded":%d}}],"UnknownTopLevel":"' "$local_counter"
                cat "$padding_file"
                printf '"},\n'
            } >>"$report_file"
            local_records=$((local_records + 1))
            printf '%d\n' "$local_records" >"$temp_dir/writer-count"

            if ((local_records % rotate_every_records == 0)); then
                printf '{}\n]\n' >>"$report_file"
                rm -f -- "${report_base}_9.json"
                for ((rotation = 8; rotation >= 1; rotation--)); do
                    if [[ -f "${report_base}_${rotation}.json" ]]; then
                        mv -- "${report_base}_${rotation}.json" "${report_base}_$((rotation + 1)).json"
                    fi
                done
                mv -- "$report_file" "${report_base}_1.json"
                printf '[\n' >"$report_file"
                chmod 0600 "$report_file"
            fi
            sleep 1
        done
        printf '{}\n]\n' >>"$report_file"
    ) &
    writer_pid=$!
}

budget_setup
budget_start_exporter
start_load_writer
budget_print_environment load
printf 'fixture_rate_bytes_per_second=%s\n' "$load_bytes_per_second"
printf 'rotation_every_records=%s\n' "$rotate_every_records"
budget_measure 0 "$max_average_cpu" "$max_rss_bytes"
kill -0 "$writer_pid" 2>/dev/null || budget_fail "load writer stopped during measurement"
budget_stop_source
printf 'records_written=%s\n' "$(<"$temp_dir/writer-count")"