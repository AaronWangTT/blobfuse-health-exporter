#!/usr/bin/env bash

set -euo pipefail

source "$(dirname -- "${BASH_SOURCE[0]}")/common.sh"
trap budget_cleanup EXIT INT TERM

max_median_cpu=${MAX_MEDIAN_CPU_PERCENT:-1}
max_rss_bytes=${MAX_RSS_BYTES:-67108864}

budget_setup
budget_start_exporter
budget_print_environment idle
budget_measure "$max_median_cpu" 0 "$max_rss_bytes"
budget_stop_source